package release

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	migrationBaselineSchemaVersion = 1
	migrationBaselineDirectory     = "migration-baselines"
	migrationBaselineMaximumBytes  = 64 << 10
	memberLifecycleMigrationFile   = "000013_member_lifecycle.up.sql"
	memberLifecycleRollbackFile    = "000013_member_lifecycle.down.sql"
	memberLifecycleMigration       = 13
	memberLifecyclePreviousVersion = 12
)

var (
	ErrInvalidMigrationRequest = errors.New("生产迁移请求无效")
	ErrMigrationDigestMismatch = errors.New("候选迁移树摘要不一致")
	ErrMigrationState          = errors.New("生产数据库迁移状态无效")
	ErrMigrationVerification   = errors.New("生产数据库迁移核验失败")
	migrationFilePattern       = regexp.MustCompile(`^([0-9]{6})_[a-z0-9_]+\.(up|down)\.sql$`)
)

type MigrationRequest struct {
	Manifest        Manifest
	Actor           string
	MigrationsDir   string
	RecoveryPointID string
}

type MigrationBaseline struct {
	SchemaVersion           int       `json:"schema_version"`
	CurrentTargetID         string    `json:"current_target_id"`
	TargetID                string    `json:"target_id"`
	TargetSourceSHA         string    `json:"target_source_sha"`
	FromMigrationTreeSHA256 string    `json:"from_migration_tree_sha256"`
	ToMigrationTreeSHA256   string    `json:"to_migration_tree_sha256"`
	MigrationVersion        int       `json:"migration_version"`
	MigrationFileSHA256     string    `json:"migration_file_sha256"`
	RecoveryPointID         string    `json:"recovery_point_id"`
	Actor                   string    `json:"actor"`
	AppliedAt               time.Time `json:"applied_at"`
}

type MigrationRollout struct {
	Config HostConfig
	Policy ManifestPolicy
	Store  *StateStore
	Runner CommandRunner
	Locker Locker
	Now    func() time.Time
}

func (rollout *MigrationRollout) Apply(ctx context.Context, request MigrationRequest) error {
	if ctx == nil || rollout == nil || rollout.Store == nil || rollout.Runner == nil || rollout.Locker == nil {
		return ErrInvalidMigrationRequest
	}
	if err := ValidateManifest(request.Manifest, rollout.Policy); err != nil ||
		!actorPattern.MatchString(request.Actor) || !filepath.IsAbs(request.MigrationsDir) {
		return ErrInvalidMigrationRequest
	}
	recoveryPointID, err := normalizeRecoveryPointID(request.RecoveryPointID)
	if err != nil {
		return err
	}
	target, err := NewStoredRelease(request.Manifest, rollout.Policy)
	if err != nil {
		return err
	}
	digest, err := MigrationTreeDigest(request.MigrationsDir)
	if err != nil || digest != target.Compatibility.MigrationTreeSHA256 {
		return ErrMigrationDigestMismatch
	}
	targetVersion := memberLifecycleMigration
	if _, err := os.Lstat(filepath.Join(request.MigrationsDir, "000015_agent_upgrade_recovery.up.sql")); err == nil {
		targetVersion = 15
	}
	migrationPath, err := validateMemberLifecycleMigrationTree(request.MigrationsDir, targetVersion)
	if err != nil {
		return err
	}
	migrationSQL, err := readRegularFile(migrationPath, 1<<20)
	if err != nil {
		return fmt.Errorf("读取成员生命周期迁移：%w", err)
	}
	if targetVersion == 15 {
		for _, name := range []string{"000014_agent_upgrade_management.up.sql", "000015_agent_upgrade_recovery.up.sql"} {
			body, err := readRegularFile(filepath.Join(request.MigrationsDir, name), 1<<20)
			if err != nil {
				return fmt.Errorf("读取代理升级迁移：%w", err)
			}
			migrationSQL = append(migrationSQL, '\n')
			migrationSQL = append(migrationSQL, body...)
		}
	}
	migrationSHA := sha256.Sum256(migrationSQL)

	config := normalizeHostConfig(rollout.Config)
	releaseLock, err := rollout.Locker.TryLock(filepath.Join(rollout.Store.root, "release.lock"))
	if err != nil {
		return fmt.Errorf("获取生产发布锁：%w", err)
	}
	if releaseLock == nil {
		return errors.New("获取生产发布锁：释放函数为空")
	}
	defer func() { _ = releaseLock() }()

	current, err := rollout.Store.LoadCurrent()
	if err != nil {
		return fmt.Errorf("读取当前成功版本：%w", err)
	}
	if !sameNonMigrationCompatibility(current.Compatibility, target.Compatibility) ||
		current.Compatibility.MigrationTreeSHA256 == target.Compatibility.MigrationTreeSHA256 {
		return ErrIncompatibleRelease
	}
	historicalDigest, err := migrationTreeDigestThrough(request.MigrationsDir, memberLifecyclePreviousVersion)
	if err != nil || historicalDigest != current.Compatibility.MigrationTreeSHA256 {
		return ErrMigrationDigestMismatch
	}
	preflight, err := runMigrationSQL(ctx, rollout.Runner, config, migrationPreflightSQL(recoveryPointID))
	if err != nil {
		return fmt.Errorf("读取迁移前数据库状态：%w", err)
	}
	preState, err := parseMigrationPreflight(preflight.Stdout)
	if err != nil {
		return err
	}
	if preState.version != memberLifecyclePreviousVersion && preState.version != targetVersion {
		return fmt.Errorf("%w：当前版本为 %d", ErrMigrationState, preState.version)
	}
	if !preState.recoveryPointVerified {
		return fmt.Errorf("%w：恢复点未成功完成隔离恢复核验", ErrMigrationState)
	}
	if preState.version == memberLifecyclePreviousVersion {
		transaction := append([]byte("BEGIN;\n"), migrationSQL...)
		if len(transaction) == 0 || transaction[len(transaction)-1] != '\n' {
			transaction = append(transaction, '\n')
		}
		transaction = append(transaction, []byte("COMMIT;\n")...)
		if _, err := runMigrationSQL(ctx, rollout.Runner, config, transaction); err != nil {
			return fmt.Errorf("执行成员生命周期迁移：%w", err)
		}
	}
	verification, err := runMigrationSQL(ctx, rollout.Runner, config, migrationVerificationSQL(targetVersion))
	if err != nil {
		return fmt.Errorf("读取迁移后数据库状态：%w", err)
	}
	if err := verifyMigrationState(verification.Stdout, preState, targetVersion); err != nil {
		return err
	}
	if digestAfter, err := MigrationTreeDigest(request.MigrationsDir); err != nil || digestAfter != digest {
		return ErrMigrationDigestMismatch
	}

	now := time.Now
	if rollout.Now != nil {
		now = rollout.Now
	}
	baseline := MigrationBaseline{
		SchemaVersion: migrationBaselineSchemaVersion, CurrentTargetID: current.TargetID,
		TargetID: target.TargetID, TargetSourceSHA: target.SourceSHA,
		FromMigrationTreeSHA256: current.Compatibility.MigrationTreeSHA256,
		ToMigrationTreeSHA256:   target.Compatibility.MigrationTreeSHA256,
		MigrationVersion:        targetVersion, MigrationFileSHA256: hex.EncodeToString(migrationSHA[:]),
		RecoveryPointID: recoveryPointID, Actor: request.Actor, AppliedAt: now().UTC(),
	}
	return rollout.Store.SaveMigrationBaseline(baseline)
}

type migrationPreState struct {
	version               int
	recoveryPointVerified bool
	users                 int64
	liveSessions          int64
}

func parseMigrationPreflight(output []byte) (migrationPreState, error) {
	fields := strings.Split(strings.TrimSpace(string(output)), "|")
	if len(fields) != 4 {
		return migrationPreState{}, ErrMigrationState
	}
	version, versionErr := strconv.Atoi(fields[0])
	users, usersErr := strconv.ParseInt(fields[2], 10, 64)
	sessions, sessionsErr := strconv.ParseInt(fields[3], 10, 64)
	if versionErr != nil || usersErr != nil || sessionsErr != nil || users < 0 || sessions < 0 || (fields[1] != "t" && fields[1] != "f") {
		return migrationPreState{}, ErrMigrationState
	}
	return migrationPreState{version: version, recoveryPointVerified: fields[1] == "t", users: users, liveSessions: sessions}, nil
}

func verifyMigrationState(output []byte, before migrationPreState, target ...int) error {
	version := memberLifecycleMigration
	if len(target) > 0 {
		version = target[0]
	}
	fields := strings.Split(strings.TrimSpace(string(output)), "|")
	if len(fields) != 6 || fields[0] != strconv.Itoa(version) ||
		fields[1] != "t" || fields[2] != "t" || fields[3] != "t" ||
		fields[4] != strconv.FormatInt(before.users, 10) || fields[5] != strconv.FormatInt(before.liveSessions, 10) {
		return ErrMigrationVerification
	}
	return nil
}

func migrationPreflightSQL(recoveryPointID string) []byte {
	return []byte(fmt.Sprintf(`SELECT concat(
  COALESCE((SELECT max(version) FROM schema_migrations),0), '|',
  EXISTS(
    SELECT 1 FROM backup_runs b
    JOIN restore_verifications v ON v.backup_run_id=b.id
    WHERE b.id='%s'::uuid AND b.status='succeeded'
      AND b.local_snapshot_id<>'' AND b.cos_snapshot_id<>''
      AND b.manifest_sha256 ~ '^[0-9a-f]{64}$'
      AND v.status='succeeded' AND v.migration_version='12'
  ), '|',
  (SELECT count(*) FROM users), '|',
  (SELECT count(*) FROM sessions WHERE revoked_at IS NULL AND expires_at>now())
);
`, recoveryPointID))
}

func migrationVerificationSQL(target ...int) []byte {
	sql := `SELECT concat(
  COALESCE((SELECT max(version) FROM schema_migrations),0), '|',
  EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='users' AND column_name='must_change_password'), '|',
  EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='users' AND column_name='removed_at'), '|',
  to_regclass('public.users_removed_at_idx') IS NOT NULL, '|',
  (SELECT count(*) FROM users), '|',
  (SELECT count(*) FROM sessions WHERE revoked_at IS NULL AND expires_at>now())
);
`
	if len(target) > 0 && target[0] == 15 {
		checks := []string{"to_regclass('public.users_removed_at_idx') IS NOT NULL"}
		for _, table := range []string{"agent_releases", "agent_release_artifacts", "agent_upgrade_plans", "agent_upgrade_targets", "agent_upgrade_events"} {
			checks = append(checks, fmt.Sprintf("to_regclass('public.%s') IS NOT NULL", table))
		}
		for _, column := range []string{"agent_os", "agent_arch", "agent_capabilities"} {
			checks = append(checks, fmt.Sprintf("EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='servers' AND column_name='%s')", column))
		}
		checks = append(checks, "EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='agent_upgrade_targets' AND column_name='install_command_id' AND is_nullable='NO')")
		for _, index := range []string{"agent_releases_one_recommended_idx", "agent_upgrade_plans_one_active_idx", "agent_upgrade_events_command_stage_idx"} {
			checks = append(checks, fmt.Sprintf("EXISTS(SELECT 1 FROM pg_index WHERE indexrelid=to_regclass('public.%s') AND indisunique AND indisvalid)", index))
		}
		checks = append(checks, "(SELECT count(*)=15 AND min(version)=1 AND max(version)=15 FROM schema_migrations)")
		sql = strings.Replace(sql, "to_regclass('public.users_removed_at_idx') IS NOT NULL", "("+strings.Join(checks, " AND ")+")", 1)
	}
	return []byte(sql)
}

func runMigrationSQL(ctx context.Context, runner CommandRunner, config HostConfig, input []byte) (CommandResult, error) {
	args := append(composePrefix(config), "exec", "-T", "postgres", "sh", "-ceu",
		`exec psql --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" --no-psqlrc --set ON_ERROR_STOP=1 --tuples-only --no-align`)
	result, err := runner.Run(ctx, "docker", args, input)
	if err != nil || result.ExitCode != 0 {
		if err == nil {
			err = fmt.Errorf("命令退出码为 %d", result.ExitCode)
		}
		return result, err
	}
	return result, nil
}

func validateMemberLifecycleMigrationTree(root string, target ...int) (string, error) {
	maximum := memberLifecycleMigration
	if len(target) > 0 {
		maximum = target[0]
	}
	if maximum != 13 && maximum != 15 {
		return "", ErrInvalidMigrationRequest
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", ErrInvalidMigrationRequest
	}
	versions := make(map[int]map[string]string)
	for _, entry := range entries {
		matches := migrationFilePattern.FindStringSubmatch(entry.Name())
		info, infoErr := entry.Info()
		if len(matches) == 0 || infoErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return "", ErrInvalidMigrationRequest
		}
		version, err := strconv.Atoi(matches[1])
		if err != nil || version < 1 || version > maximum {
			return "", ErrInvalidMigrationRequest
		}
		if versions[version] == nil {
			versions[version] = make(map[string]string)
		}
		direction := matches[2]
		if versions[version][direction] != "" {
			return "", ErrInvalidMigrationRequest
		}
		versions[version][direction] = entry.Name()
	}
	for version := 1; version <= maximum; version++ {
		if versions[version]["up"] == "" || versions[version]["down"] == "" {
			return "", ErrInvalidMigrationRequest
		}
	}
	if len(entries) != maximum*2 || len(versions) != maximum ||
		versions[memberLifecycleMigration]["up"] != memberLifecycleMigrationFile ||
		versions[memberLifecycleMigration]["down"] != memberLifecycleRollbackFile {
		return "", ErrInvalidMigrationRequest
	}
	if maximum == 15 {
		for version, name := range map[int]string{14: "agent_upgrade_management", 15: "agent_upgrade_recovery"} {
			for _, direction := range []string{"up", "down"} {
				if versions[version][direction] != fmt.Sprintf("%06d_%s.%s.sql", version, name, direction) {
					return "", ErrInvalidMigrationRequest
				}
			}
		}
	}
	return filepath.Join(root, memberLifecycleMigrationFile), nil
}

func migrationTreeDigestThrough(root string, maximumVersion int) (string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", err
	}
	digests := make([]digestEntry, 0, len(entries))
	for _, entry := range entries {
		matches := migrationFilePattern.FindStringSubmatch(entry.Name())
		if len(matches) == 0 {
			return "", ErrInvalidMigrationRequest
		}
		version, err := strconv.Atoi(matches[1])
		if err != nil {
			return "", ErrInvalidMigrationRequest
		}
		if version > maximumVersion {
			continue
		}
		digest, err := FileSHA256(filepath.Join(root, entry.Name()))
		if err != nil {
			return "", err
		}
		digests = append(digests, digestEntry{path: filepath.ToSlash(entry.Name()), digest: digest})
	}
	if len(digests) == 0 {
		return "", ErrInvalidMigrationRequest
	}
	return digestEntries(digests)
}

func normalizeRecoveryPointID(value string) (string, error) {
	id, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil {
		return "", ErrInvalidMigrationRequest
	}
	return id.String(), nil
}

func readRegularFile(path string, maximum int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maximum {
		return nil, ErrInvalidMigrationRequest
	}
	return os.ReadFile(path)
}

func sameNonMigrationCompatibility(first, second Compatibility) bool {
	return first.DeploymentContractSHA256 == second.DeploymentContractSHA256 &&
		first.AgentVersion == second.AgentVersion && first.AgentManifestSHA256 == second.AgentManifestSHA256
}

func (store *StateStore) SaveMigrationBaseline(baseline MigrationBaseline) error {
	if store == nil || validateMigrationBaseline(baseline) != nil {
		return ErrInvalidMigrationRequest
	}
	current, err := store.LoadCurrent()
	if err != nil {
		return err
	}
	if baseline.CurrentTargetID != current.TargetID || baseline.FromMigrationTreeSHA256 != current.Compatibility.MigrationTreeSHA256 {
		return ErrInvalidMigrationRequest
	}
	if err := store.ensureRoot(); err != nil {
		return err
	}
	directory := filepath.Join(store.root, migrationBaselineDirectory)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("创建迁移基线目录：%w", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return fmt.Errorf("限制迁移基线目录权限：%w", err)
	}
	path := filepath.Join(directory, baseline.TargetID+".json")
	data, err := marshalJSONLine(baseline)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".migration-baseline-"+baseline.TargetID+"-")
	if err != nil {
		return fmt.Errorf("创建迁移基线临时文件：%w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("限制迁移基线临时文件权限：%w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return fmt.Errorf("写入迁移基线：%w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("同步迁移基线：%w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("关闭迁移基线：%w", err)
	}
	if err := store.publishMigrationBaseline(temporaryPath, path, baseline); err != nil {
		return err
	}
	if err := os.Remove(temporaryPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("清理迁移基线临时文件：%w", err)
	}
	return syncDirectory(directory)
}

type migrationBaselineFileState uint8

const (
	migrationBaselineAbsent migrationBaselineFileState = iota
	migrationBaselineRecoverable
	migrationBaselineMatching
	migrationBaselineConflict
)

func (store *StateStore) publishMigrationBaseline(temporaryPath, path string, baseline MigrationBaseline) error {
	for attempt := 0; attempt < 3; attempt++ {
		state, err := store.inspectMigrationBaseline(path, baseline)
		if err != nil {
			return err
		}
		switch state {
		case migrationBaselineMatching:
			return nil
		case migrationBaselineConflict:
			return ErrReleaseExists
		case migrationBaselineAbsent:
			if err := os.Link(temporaryPath, path); err == nil {
				return nil
			} else if errors.Is(err, os.ErrExist) {
				continue
			} else {
				return fmt.Errorf("原子发布迁移基线：%w", err)
			}
		case migrationBaselineRecoverable:
			return store.replaceRecoverableMigrationBaseline(temporaryPath, path, baseline)
		}
	}
	return ErrReleaseExists
}

func (store *StateStore) inspectMigrationBaseline(path string, baseline MigrationBaseline) (migrationBaselineFileState, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return migrationBaselineAbsent, nil
	}
	if err != nil {
		return migrationBaselineConflict, fmt.Errorf("读取迁移基线目标：%w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > migrationBaselineMaximumBytes {
		return migrationBaselineConflict, nil
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return migrationBaselineConflict, fmt.Errorf("读取迁移基线目标：%w", err)
	}
	var existing MigrationBaseline
	if err := decodeStrictJSON(bytes.NewReader(body), &existing); err != nil || validateMigrationBaseline(existing) != nil {
		return migrationBaselineRecoverable, nil
	}
	if sameMigrationBaselineIdentity(existing, baseline) {
		return migrationBaselineMatching, nil
	}
	return migrationBaselineConflict, nil
}

func (store *StateStore) replaceRecoverableMigrationBaseline(temporaryPath, path string, baseline MigrationBaseline) error {
	if err := os.Rename(temporaryPath, path); err == nil {
		return nil
	} else if runtime.GOOS != "windows" {
		return fmt.Errorf("原子替换损坏迁移基线：%w", err)
	}

	state, err := store.inspectMigrationBaseline(path, baseline)
	if err != nil {
		return err
	}
	switch state {
	case migrationBaselineMatching:
		return nil
	case migrationBaselineConflict:
		return ErrReleaseExists
	case migrationBaselineAbsent:
		if err := os.Rename(temporaryPath, path); err != nil {
			return fmt.Errorf("发布恢复后的迁移基线：%w", err)
		}
		return nil
	case migrationBaselineRecoverable:
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("移除损坏迁移基线：%w", err)
		}
		if err := os.Rename(temporaryPath, path); err != nil {
			return fmt.Errorf("发布恢复后的迁移基线：%w", err)
		}
		return nil
	default:
		return ErrReleaseExists
	}
}

func (store *StateStore) LoadMigrationBaseline(targetID string) (MigrationBaseline, error) {
	if store == nil || !targetIDPattern.MatchString(targetID) {
		return MigrationBaseline{}, ErrInvalidMigrationRequest
	}
	body, err := readRegularFile(filepath.Join(store.root, migrationBaselineDirectory, targetID+".json"), migrationBaselineMaximumBytes)
	if err != nil {
		return MigrationBaseline{}, err
	}
	var baseline MigrationBaseline
	if err := decodeStrictJSON(bytes.NewReader(body), &baseline); err != nil {
		return MigrationBaseline{}, ErrInvalidMigrationRequest
	}
	if err := validateMigrationBaseline(baseline); err != nil {
		return MigrationBaseline{}, err
	}
	return baseline, nil
}

func (store *StateStore) MigrationBaselineAllows(current, target StoredRelease) bool {
	if store == nil || !sameNonMigrationCompatibility(current.Compatibility, target.Compatibility) {
		return false
	}
	baseline, err := store.LoadMigrationBaseline(target.TargetID)
	if err != nil {
		return false
	}
	return baseline.CurrentTargetID == current.TargetID && baseline.TargetID == target.TargetID &&
		baseline.TargetSourceSHA == target.SourceSHA &&
		baseline.FromMigrationTreeSHA256 == current.Compatibility.MigrationTreeSHA256 &&
		baseline.ToMigrationTreeSHA256 == target.Compatibility.MigrationTreeSHA256
}

func validateMigrationBaseline(baseline MigrationBaseline) error {
	if baseline.SchemaVersion != migrationBaselineSchemaVersion || !validTargetID(baseline.CurrentTargetID) ||
		!targetIDPattern.MatchString(baseline.TargetID) || !lowerHex40Pattern.MatchString(baseline.TargetSourceSHA) ||
		!lowerHex64Pattern.MatchString(baseline.FromMigrationTreeSHA256) || !lowerHex64Pattern.MatchString(baseline.ToMigrationTreeSHA256) ||
		baseline.FromMigrationTreeSHA256 == baseline.ToMigrationTreeSHA256 || (baseline.MigrationVersion != memberLifecycleMigration && baseline.MigrationVersion != 15) ||
		!lowerHex64Pattern.MatchString(baseline.MigrationFileSHA256) || !actorPattern.MatchString(baseline.Actor) ||
		!isUTCNonZero(baseline.AppliedAt) {
		return ErrInvalidMigrationRequest
	}
	if _, err := normalizeRecoveryPointID(baseline.RecoveryPointID); err != nil {
		return err
	}
	return nil
}

func sameMigrationBaselineIdentity(first, second MigrationBaseline) bool {
	first.AppliedAt = time.Time{}
	second.AppliedAt = time.Time{}
	return first == second
}
