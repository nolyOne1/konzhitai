package release

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

const platformFeatureMigration = 19

var platformMigrationNames = map[int]string{16: "server_groups", 17: "script_sync_retries", 18: "unlimited_task_wait", 19: "log_archive_progress"}

// This is a separate, bounded 15 -> 19 rollout. Existing 12 -> 13/15
// candidates and their immutable baseline identities keep their old rules.
func (rollout *MigrationRollout) applyPlatformMigration(ctx context.Context, request MigrationRequest) error {
	recoveryPoint, err := normalizeRecoveryPointID(request.RecoveryPointID)
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
	if _, err := validateMemberLifecycleMigrationTree(request.MigrationsDir, platformFeatureMigration); err != nil {
		return err
	}
	var pending []byte
	for version := 16; version <= platformFeatureMigration; version++ {
		body, err := readRegularFile(filepath.Join(request.MigrationsDir, fmt.Sprintf("%06d_%s.up.sql", version, platformMigrationNames[version])), 1<<20)
		if err != nil {
			return err
		}
		if len(pending) > 0 {
			pending = append(pending, '\n')
		}
		pending = append(pending, body...)
	}
	pendingSHA := sha256.Sum256(pending)
	config := normalizeHostConfig(rollout.Config)
	unlock, err := rollout.Locker.TryLock(filepath.Join(rollout.Store.root, "release.lock"))
	if err != nil {
		return fmt.Errorf("获取生产发布锁：%w", err)
	}
	if unlock == nil {
		return errors.New("获取生产发布锁：释放函数为空")
	}
	defer func() { _ = unlock() }()
	current, err := rollout.Store.LoadCurrent()
	if err != nil {
		return err
	}
	if !sameNonMigrationCompatibility(current.Compatibility, target.Compatibility) || current.Compatibility.MigrationTreeSHA256 == digest {
		return ErrIncompatibleRelease
	}
	matching, err := historicalMigrationDigestMatches(request.MigrationsDir, current.Compatibility.MigrationTreeSHA256, 15)
	if err != nil || !matching {
		return ErrMigrationDigestMismatch
	}
	receipt := platformMigrationReceipt(current, target, recoveryPoint, request.Actor)
	preflight, err := runMigrationSQL(ctx, rollout.Runner, config, platformMigrationPreflightSQL(recoveryPoint, receipt))
	if err != nil {
		return fmt.Errorf("读取功能迁移前状态：%w", err)
	}
	fields := strings.Split(strings.TrimSpace(string(preflight.Stdout)), "|")
	if len(fields) != 5 || (fields[4] != "t" && fields[4] != "f") {
		return ErrMigrationState
	}
	before, err := parseMigrationPreflight([]byte(strings.Join(fields[:4], "|")))
	if err != nil {
		return err
	}
	if !before.recoveryPointVerified || (before.version != 15 && before.version != platformFeatureMigration) {
		return fmt.Errorf("%w：仅支持有版本 15 恢复点的 15→19 迁移", ErrMigrationState)
	}
	// The receipt is committed in the same transaction as the schema changes.
	// A crashed invocation can resume only this exact candidate and recovery point.
	if (before.version == platformFeatureMigration && fields[4] != "t") || (before.version == 15 && fields[4] != "f") {
		return fmt.Errorf("%w：迁移回执不属于当前候选", ErrMigrationState)
	}
	if before.version == 15 {
		transaction := platformMigrationTransaction(pending, receipt)
		if _, err := runMigrationSQL(ctx, rollout.Runner, config, transaction); err != nil {
			return fmt.Errorf("执行版本 15→19 功能迁移：%w", err)
		}
	}
	verification, err := runMigrationSQL(ctx, rollout.Runner, config, platformMigrationVerificationSQL(receipt))
	if err != nil {
		return fmt.Errorf("读取功能迁移后状态：%w", err)
	}
	if err := verifyMigrationState(verification.Stdout, before, platformFeatureMigration); err != nil {
		return err
	}
	if after, err := MigrationTreeDigest(request.MigrationsDir); err != nil || after != digest {
		return ErrMigrationDigestMismatch
	}
	now := time.Now
	if rollout.Now != nil {
		now = rollout.Now
	}
	return rollout.Store.SaveMigrationBaseline(MigrationBaseline{
		SchemaVersion: migrationBaselineSchemaVersion, CurrentTargetID: current.TargetID, TargetID: target.TargetID, TargetSourceSHA: target.SourceSHA,
		FromMigrationTreeSHA256: current.Compatibility.MigrationTreeSHA256, ToMigrationTreeSHA256: digest,
		MigrationVersion: platformFeatureMigration, MigrationFileSHA256: hex.EncodeToString(pendingSHA[:]),
		RecoveryPointID: recoveryPoint, Actor: request.Actor, AppliedAt: now().UTC(),
	})
}

func platformMigrationReceipt(current, target StoredRelease, recoveryPoint, actor string) string {
	body, _ := json.Marshal(map[string]any{"from_version": 15, "to_version": platformFeatureMigration, "current_target": current.TargetID, "candidate": target.TargetID, "source_sha": target.SourceSHA, "from_tree": current.Compatibility.MigrationTreeSHA256, "to_tree": target.Compatibility.MigrationTreeSHA256, "recovery_point": recoveryPoint, "operator": actor})
	return string(body)
}
func sqlText(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }
func platformReceiptExists(receipt string) string {
	return "EXISTS(SELECT 1 FROM audit_logs WHERE action='release.migration.apply' AND target_type='release' AND details=" + sqlText(receipt) + "::jsonb)"
}
func platformMigrationPreflightSQL(recoveryPoint, receipt string) []byte {
	return []byte(fmt.Sprintf(`SELECT concat(
  COALESCE((SELECT max(version) FROM schema_migrations),0),'|',
  EXISTS(SELECT 1 FROM backup_runs b JOIN restore_verifications v ON v.backup_run_id=b.id
    WHERE b.id=%s::uuid AND b.status='succeeded' AND b.local_snapshot_id<>'' AND b.cos_snapshot_id<>''
      AND b.manifest_sha256 ~ '^[0-9a-f]{64}$' AND v.status='succeeded' AND v.migration_version='15')
  AND (SELECT min(version)=1 AND (count(*)=15 AND max(version)=15 OR count(*)=19 AND max(version)=19) FROM schema_migrations),'|',
  (SELECT count(*) FROM users),'|',
  (SELECT count(*) FROM sessions WHERE revoked_at IS NULL AND expires_at>now()),'|',%s);
`, sqlText(recoveryPoint), platformReceiptExists(receipt)))
}
func platformMigrationTransaction(pending []byte, receipt string) []byte {
	return []byte("BEGIN;\nSET LOCAL lock_timeout='10s';\nLOCK TABLE schema_migrations IN EXCLUSIVE MODE;\n" +
		"DO $yunling$ BEGIN IF NOT (SELECT count(*)=15 AND min(version)=1 AND max(version)=15 FROM schema_migrations) THEN RAISE EXCEPTION 'migration baseline changed'; END IF; END $yunling$;\n" +
		string(pending) + "\nINSERT INTO audit_logs(action,target_type,target_id,details) VALUES('release.migration.apply','release'," + sqlText(receipt) + "::jsonb->>'candidate'," + sqlText(receipt) + "::jsonb);\nCOMMIT;\n")
}
func platformMigrationVerificationSQL(receipt string) []byte {
	base := string(migrationVerificationSQL(15))
	base = strings.Replace(base, "count(*)=15 AND min(version)=1 AND max(version)=15", "count(*)=19 AND min(version)=1 AND max(version)=19", 1)
	checks := []string{platformReceiptExists(receipt), "to_regclass('public.server_groups') IS NOT NULL",
		"NOT EXISTS(SELECT 1 FROM servers s WHERE s.server_group_id<>'' AND NOT EXISTS(SELECT 1 FROM server_groups g WHERE g.id=s.server_group_id))"}
	for _, column := range [][2]string{{"server_groups", "name"}, {"script_syncs", "failure_count"}, {"script_syncs", "next_retry_at"}, {"log_chunks", "archive_cursor"}, {"log_chunks", "received_at"}, {"run_log_archives", "last_log_cursor"}, {"run_log_archives", "chunk_count"}} {
		checks = append(checks, fmt.Sprintf("EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='%s' AND column_name='%s')", column[0], column[1]))
	}
	for _, index := range []string{"script_syncs_retry_idx", "log_chunks_archive_cursor_idx"} {
		checks = append(checks, fmt.Sprintf("EXISTS(SELECT 1 FROM pg_index WHERE indexrelid=to_regclass('public.%s') AND indisvalid)", index))
	}
	for _, table := range []string{"task_definitions", "task_runs"} {
		checks = append(checks, fmt.Sprintf("EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='public.%s'::regclass AND conname='%s_max_wait_seconds_check' AND convalidated AND pg_get_constraintdef(oid)='CHECK ((max_wait_seconds >= 0))')", table, table))
		checks = append(checks, fmt.Sprintf("EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='%s' AND column_name='max_wait_seconds' AND column_default='0')", table))
	}
	return []byte(strings.Replace(base, "to_regclass('public.users_removed_at_idx') IS NOT NULL", "to_regclass('public.users_removed_at_idx') IS NOT NULL AND ("+strings.Join(checks, " AND ")+")", 1))
}
