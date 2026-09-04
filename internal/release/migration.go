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
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	migrationBaselineSchemaVersion = 1
	migrationBaselineDirectory     = "migration-baselines"
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
	migrationPath, err := validateMemberLifecycleMigrationTree(request.MigrationsDir)
	if err != nil {
		return err
	}
	migrationSQL, err := readRegularFile(migrationPath, 1<<20)
	if err != nil {
		return fmt.Errorf("读取成员生命周期迁移：%w", err)
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
	if preState.version != memberLifecyclePreviousVersion && preState.version != memberLifecycleMigration {
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
	verification, err := runMigrationSQL(ctx, rollout.Runner, config, migrationVerificationSQL())
	if err != nil {
		return fmt.Errorf("读取迁移后数据库状态：%w", err)
	}
	if err := verifyMigrationState(verification.Stdout, preState); err != nil {
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
		MigrationVersion:        memberLifecycleMigration, MigrationFileSHA256: hex.EncodeToString(migrationSHA[:]),
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

func verifyMigrationState(output []byte, before migrationPreState) error {
	fields := strings.Split(strings.TrimSpace(string(output)), "|")
	if len(fields) != 6 || fields[0] != strconv.Itoa(memberLifecycleMigration) ||
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

func migrationVerificationSQL() []byte {
	return []byte(`SELECT concat(
  COALESCE((SELECT max(version) FROM schema_migrations),0), '|',
  EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='users' AND column_name='must_change_password'), '|',
  EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='users' AND column_name='removed_at'), '|',
  to_regclass('public.users_removed_at_idx') IS NOT NULL, '|',
  (SELECT count(*) FROM users), '|',
  (SELECT count(*) FROM sessions WHERE revoked_at IS NULL AND expires_at>now())
);
`)
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

func validateMemberLifecycleMigrationTree(root string) (string, error) {
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
		if err != nil || version < 1 || version > memberLifecycleMigration {
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
	for version := 1; version <= memberLifecycleMigration; version++ {
		if versions[version]["up"] == "" || versions[version]["down"] == "" {
			return "", ErrInvalidMigrationRequest
		}
	}
	if len(entries) != memberLifecycleMigration*2 || len(versions) != memberLifecycleMigration ||
		versions[memberLifecycleMigration]["up"] != memberLifecycleMigrationFile ||
		versions[memberLifecycleMigration]["down"] != memberLifecycleRollbackFile {
		return "", ErrInvalidMigrationRequest
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
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, os.ErrExist) {
		existing, readErr := store.LoadMigrationBaseline(baseline.TargetID)
		if readErr == nil && sameMigrationBaselineIdentity(existing, baseline) {
			return nil
		}
		return ErrReleaseExists
	}
	if err != nil {
		return fmt.Errorf("创建迁移基线：%w", err)
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return fmt.Errorf("写入迁移基线：%w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return fmt.Errorf("同步迁移基线：%w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("关闭迁移基线：%w", err)
	}
	return syncDirectory(directory)
}

func (store *StateStore) LoadMigrationBaseline(targetID string) (MigrationBaseline, error) {
	if store == nil || !targetIDPattern.MatchString(targetID) {
		return MigrationBaseline{}, ErrInvalidMigrationRequest
	}
	body, err := readRegularFile(filepath.Join(store.root, migrationBaselineDirectory, targetID+".json"), 64<<10)
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
		baseline.FromMigrationTreeSHA256 == baseline.ToMigrationTreeSHA256 || baseline.MigrationVersion != memberLifecycleMigration ||
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
