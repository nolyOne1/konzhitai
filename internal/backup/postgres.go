package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresRepository struct{ db *pgxpool.Pool }

func NewPostgresRepository(db *pgxpool.Pool) *PostgresRepository {
	return &PostgresRepository{db: db}
}

func (r *PostgresRepository) EnsureSchedules(ctx context.Context, now time.Time) error {
	if r == nil || r.db == nil {
		return ErrUnavailable
	}
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return fmt.Errorf("加载备份时区：%w", err)
	}
	localNow := now.In(location)
	start := now.Add(-6 * time.Hour)
	end := now.Add(24 * time.Hour)
	for dayOffset := -1; dayOffset <= 1; dayOffset++ {
		day := localNow.AddDate(0, 0, dayOffset)
		for _, hour := range []int{0, 6, 12, 18} {
			slot := time.Date(day.Year(), day.Month(), day.Day(), hour, 30, 0, 0, location).UTC()
			if slot.Before(start) || slot.After(end) {
				continue
			}
			if _, err := r.RequestBackup(ctx, "", "", slot); err != nil {
				return err
			}
		}
	}
	verificationSlot := time.Date(localNow.Year(), localNow.Month(), 1, 3, 30, 0, 0, location).UTC()
	if !verificationSlot.After(now) {
		var backupID string
		err := r.db.QueryRow(ctx, `
			SELECT id::text FROM backup_runs
			WHERE status='succeeded' AND cos_snapshot_id<>''
			ORDER BY finished_at DESC NULLS LAST, created_at DESC LIMIT 1
		`).Scan(&backupID)
		if err != nil && err != pgx.ErrNoRows {
			return fmt.Errorf("选择恢复校验备份：%w", err)
		}
		if err == nil {
			if _, err := r.RequestVerification(ctx, "", backupID, "", verificationSlot); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *PostgresRepository) RequestBackup(ctx context.Context, actorID, idempotencyKey string, at time.Time) (BackupRun, error) {
	if r == nil || r.db == nil || at.IsZero() {
		return BackupRun{}, ErrInvalidRequest
	}
	manual := strings.TrimSpace(actorID) != "" || strings.TrimSpace(idempotencyKey) != ""
	if manual {
		if _, err := uuid.Parse(actorID); err != nil {
			return BackupRun{}, ErrInvalidRequest
		}
		if _, err := uuid.Parse(idempotencyKey); err != nil {
			return BackupRun{}, ErrInvalidRequest
		}
	} else {
		actorID, idempotencyKey = "", ""
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return BackupRun{}, fmt.Errorf("开始创建备份事务：%w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	lockKey := "backup:scheduled:" + at.UTC().Format(time.RFC3339Nano)
	if manual {
		lockKey = "backup:manual:" + idempotencyKey
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockKey); err != nil {
		return BackupRun{}, fmt.Errorf("锁定备份请求：%w", err)
	}
	var run BackupRun
	if manual {
		err = scanBackup(tx.QueryRow(ctx, backupSelect+` WHERE idempotency_key=$1`, idempotencyKey), &run)
	} else {
		err = scanBackup(tx.QueryRow(ctx, backupSelect+` WHERE scheduled_for=$1`, at), &run)
	}
	if err == nil {
		if err := tx.Commit(ctx); err != nil {
			return BackupRun{}, fmt.Errorf("提交幂等备份事务：%w", err)
		}
		return run, nil
	}
	if err != pgx.ErrNoRows {
		return BackupRun{}, fmt.Errorf("查找幂等备份：%w", err)
	}
	if manual {
		err = scanBackup(tx.QueryRow(ctx, backupInsert+`
			VALUES ('manual', 'queued', NULL, $1, $2, $3, $3, $3) RETURNING `+backupColumns,
			idempotencyKey, actorID, at), &run)
	} else {
		err = scanBackup(tx.QueryRow(ctx, backupInsert+`
			VALUES ('scheduled', 'queued', $1, NULL, NULL, $1, $1, $1) RETURNING `+backupColumns,
			at), &run)
	}
	if err != nil {
		return BackupRun{}, fmt.Errorf("创建备份记录：%w", err)
	}
	if manual {
		if err := insertAudit(ctx, tx, actorID, "operations.backup.request", "backup_run", run.ID, at); err != nil {
			return BackupRun{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return BackupRun{}, fmt.Errorf("提交备份请求：%w", err)
	}
	return run, nil
}

func (r *PostgresRepository) ClaimBackup(ctx context.Context, now time.Time, lease time.Duration) (BackupRun, bool, error) {
	if r == nil || r.db == nil || lease <= 0 {
		return BackupRun{}, false, ErrInvalidRequest
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return BackupRun{}, false, fmt.Errorf("开始领取备份事务：%w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('yunling:backup:lease', 0))`); err != nil {
		return BackupRun{}, false, fmt.Errorf("锁定备份租约：%w", err)
	}
	var run BackupRun
	err = scanBackup(tx.QueryRow(ctx, `
		WITH candidate AS (
			SELECT id FROM backup_runs
			WHERE ((status IN ('exporting','snapshotting','uploading') AND lease_until <= $1)
			   OR (status IN ('queued','degraded') AND next_attempt_at <= $1))
			ORDER BY CASE WHEN status IN ('exporting','snapshotting','uploading') THEN 0 ELSE 1 END,
			         next_attempt_at, created_at, id
			FOR UPDATE SKIP LOCKED LIMIT 1
		)
		UPDATE backup_runs AS run
		SET status=CASE WHEN run.local_snapshot_id<>'' THEN 'uploading' ELSE 'exporting' END,
		    attempts=run.attempts+1, lease_until=$1+$2::interval,
		    started_at=COALESCE(run.started_at,$1), error_message='', updated_at=$1
		FROM candidate WHERE run.id=candidate.id RETURNING `+backupReturningColumns,
		now, lease.String()), &run)
	if err == pgx.ErrNoRows {
		if err := tx.Commit(ctx); err != nil {
			return BackupRun{}, false, fmt.Errorf("提交空备份领取：%w", err)
		}
		return BackupRun{}, false, nil
	}
	if err != nil {
		return BackupRun{}, false, fmt.Errorf("领取备份：%w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return BackupRun{}, false, fmt.Errorf("提交备份领取：%w", err)
	}
	return run, true, nil
}

func (r *PostgresRepository) MarkLocalSnapshot(ctx context.Context, id string, result SnapshotResult, at time.Time) error {
	if result.SnapshotID == "" || result.ManifestSHA256 == "" || result.ByteSize < 0 || result.ObjectCount < 0 {
		return ErrInvalidRequest
	}
	return r.transition(ctx, `
		UPDATE backup_runs SET status='uploading', local_snapshot_id=$2,
		manifest_sha256=$3, byte_size=$4, object_count=$5, updated_at=$6
		WHERE id=$1 AND status IN ('exporting','snapshotting')
	`, id, result.SnapshotID, result.ManifestSHA256, result.ByteSize, result.ObjectCount, at)
}

func (r *PostgresRepository) MarkBackupSucceeded(ctx context.Context, id, cosSnapshotID string, at time.Time) error {
	if strings.TrimSpace(cosSnapshotID) == "" {
		return ErrInvalidRequest
	}
	return r.transition(ctx, `
		UPDATE backup_runs SET status='succeeded', cos_snapshot_id=$2,
		lease_until=NULL, error_message='', finished_at=$3, updated_at=$3
		WHERE id=$1 AND status='uploading' AND local_snapshot_id<>''
	`, id, cosSnapshotID, at)
}

func (r *PostgresRepository) MarkBackupDegraded(ctx context.Context, id, message string, retryAt time.Time) error {
	return r.transition(ctx, `
		UPDATE backup_runs SET status='degraded', next_attempt_at=$2,
		lease_until=NULL, error_message=$3, updated_at=$2
		WHERE id=$1 AND status='uploading' AND local_snapshot_id<>''
	`, id, retryAt, truncate(message, 4096))
}

func (r *PostgresRepository) MarkBackupFailed(ctx context.Context, id, message string, at time.Time) error {
	return r.transition(ctx, `
		UPDATE backup_runs SET status='failed', lease_until=NULL,
		error_message=$2, finished_at=$3, updated_at=$3
		WHERE id=$1 AND status IN ('exporting','snapshotting','uploading')
	`, id, truncate(message, 4096), at)
}

func (r *PostgresRepository) RequestVerification(ctx context.Context, actorID, backupRunID, idempotencyKey string, at time.Time) (RestoreVerification, error) {
	if r == nil || r.db == nil || at.IsZero() {
		return RestoreVerification{}, ErrInvalidRequest
	}
	if _, err := uuid.Parse(backupRunID); err != nil {
		return RestoreVerification{}, ErrInvalidRequest
	}
	manual := strings.TrimSpace(actorID) != "" || strings.TrimSpace(idempotencyKey) != ""
	if manual {
		if _, err := uuid.Parse(actorID); err != nil {
			return RestoreVerification{}, ErrInvalidRequest
		}
		if _, err := uuid.Parse(idempotencyKey); err != nil {
			return RestoreVerification{}, ErrInvalidRequest
		}
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return RestoreVerification{}, fmt.Errorf("开始恢复校验事务：%w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	lockKey := "verification:scheduled:" + at.UTC().Format(time.RFC3339Nano)
	if manual {
		lockKey = "verification:manual:" + idempotencyKey
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockKey); err != nil {
		return RestoreVerification{}, fmt.Errorf("锁定恢复校验请求：%w", err)
	}
	var verification RestoreVerification
	if manual {
		err = scanVerification(tx.QueryRow(ctx, verificationSelect+` WHERE idempotency_key=$1`, idempotencyKey), &verification)
	} else {
		err = scanVerification(tx.QueryRow(ctx, verificationSelect+` WHERE scheduled_for=$1`, at), &verification)
	}
	if err == nil {
		if err := tx.Commit(ctx); err != nil {
			return RestoreVerification{}, fmt.Errorf("提交幂等恢复校验：%w", err)
		}
		return verification, nil
	}
	if err != pgx.ErrNoRows {
		return RestoreVerification{}, fmt.Errorf("查找幂等恢复校验：%w", err)
	}
	if manual {
		err = scanVerification(tx.QueryRow(ctx, verificationInsert+`
			VALUES ($1, 'manual', 'queued', NULL, $2, $3, $4, $4) RETURNING `+verificationColumns,
			backupRunID, idempotencyKey, actorID, at), &verification)
	} else {
		err = scanVerification(tx.QueryRow(ctx, verificationInsert+`
			VALUES ($1, 'scheduled', 'queued', $2, NULL, NULL, $2, $2) RETURNING `+verificationColumns,
			backupRunID, at), &verification)
	}
	if err != nil {
		return RestoreVerification{}, fmt.Errorf("创建恢复校验：%w", err)
	}
	if manual {
		if err := insertAudit(ctx, tx, actorID, "operations.verification.request", "restore_verification", verification.ID, at); err != nil {
			return RestoreVerification{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return RestoreVerification{}, fmt.Errorf("提交恢复校验请求：%w", err)
	}
	return verification, nil
}

func (r *PostgresRepository) ClaimVerification(ctx context.Context, now time.Time, lease time.Duration) (RestoreVerification, bool, error) {
	if r == nil || r.db == nil || lease <= 0 {
		return RestoreVerification{}, false, ErrInvalidRequest
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return RestoreVerification{}, false, fmt.Errorf("开始领取恢复校验事务：%w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('yunling:verification:lease', 0))`); err != nil {
		return RestoreVerification{}, false, fmt.Errorf("锁定恢复校验租约：%w", err)
	}
	var verification RestoreVerification
	err = scanVerification(tx.QueryRow(ctx, `
		WITH candidate AS (
			SELECT id FROM restore_verifications
			WHERE (status='queued' AND (scheduled_for IS NULL OR scheduled_for <= $1))
			   OR (status IN ('restoring','checking') AND lease_until <= $1)
			ORDER BY CASE WHEN status IN ('restoring','checking') THEN 0 ELSE 1 END,
			         created_at, id FOR UPDATE SKIP LOCKED LIMIT 1
		)
		UPDATE restore_verifications AS verification
		SET status='restoring', lease_until=$1+$2::interval,
		    started_at=COALESCE(verification.started_at,$1), error_message='', updated_at=$1
		FROM candidate WHERE verification.id=candidate.id RETURNING `+verificationReturningColumns,
		now, lease.String()), &verification)
	if err == pgx.ErrNoRows {
		if err := tx.Commit(ctx); err != nil {
			return RestoreVerification{}, false, fmt.Errorf("提交空恢复校验领取：%w", err)
		}
		return RestoreVerification{}, false, nil
	}
	if err != nil {
		return RestoreVerification{}, false, fmt.Errorf("领取恢复校验：%w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return RestoreVerification{}, false, fmt.Errorf("提交恢复校验领取：%w", err)
	}
	return verification, true, nil
}

func (r *PostgresRepository) CompleteVerification(ctx context.Context, result VerificationResult, at time.Time) error {
	status := VerificationSucceeded
	if strings.TrimSpace(result.ErrorMessage) != "" {
		status = VerificationFailed
	}
	return r.transition(ctx, `
		UPDATE restore_verifications SET status=$2, temporary_database=$3,
		migration_version=$4, checked_objects=$5, lease_until=NULL,
		error_message=$6, finished_at=$7, updated_at=$7
		WHERE id=$1 AND status IN ('restoring','checking')
	`, result.VerificationID, status, result.TemporaryDatabase, result.MigrationVersion,
		result.CheckedObjects, truncate(result.ErrorMessage, 4096), at)
}

func (r *PostgresRepository) ListBackups(ctx context.Context, limit int) ([]BackupRun, error) {
	if r == nil || r.db == nil {
		return nil, ErrUnavailable
	}
	limit = boundedLimit(limit)
	rows, err := r.db.Query(ctx, backupSelect+` ORDER BY created_at DESC, id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("读取备份历史：%w", err)
	}
	defer rows.Close()
	items := make([]BackupRun, 0)
	for rows.Next() {
		var item BackupRun
		if err := scanBackup(rows, &item); err != nil {
			return nil, fmt.Errorf("解析备份历史：%w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *PostgresRepository) ListVerifications(ctx context.Context, limit int) ([]RestoreVerification, error) {
	if r == nil || r.db == nil {
		return nil, ErrUnavailable
	}
	limit = boundedLimit(limit)
	rows, err := r.db.Query(ctx, verificationSelect+` ORDER BY created_at DESC, id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("读取恢复校验历史：%w", err)
	}
	defer rows.Close()
	items := make([]RestoreVerification, 0)
	for rows.Next() {
		var item RestoreVerification
		if err := scanVerification(rows, &item); err != nil {
			return nil, fmt.Errorf("解析恢复校验历史：%w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *PostgresRepository) transition(ctx context.Context, query string, arguments ...any) error {
	if r == nil || r.db == nil {
		return ErrUnavailable
	}
	command, err := r.db.Exec(ctx, query, arguments...)
	if err != nil {
		return fmt.Errorf("更新备份状态：%w", err)
	}
	if command.RowsAffected() != 1 {
		return ErrInvalidTransition
	}
	return nil
}

const backupColumns = `
	id::text, trigger_type, status, scheduled_for, COALESCE(requested_by::text,''),
	local_snapshot_id, cos_snapshot_id, manifest_sha256, byte_size, object_count,
	attempts, next_attempt_at, lease_until, error_message, started_at, finished_at,
	created_at, updated_at`
const backupReturningColumns = `
	run.id::text, run.trigger_type, run.status, run.scheduled_for,
	COALESCE(run.requested_by::text,''), run.local_snapshot_id, run.cos_snapshot_id,
	run.manifest_sha256, run.byte_size, run.object_count, run.attempts,
	run.next_attempt_at, run.lease_until, run.error_message, run.started_at,
	run.finished_at, run.created_at, run.updated_at`
const backupSelect = `SELECT ` + backupColumns + ` FROM backup_runs`
const backupInsert = `INSERT INTO backup_runs (
	trigger_type, status, scheduled_for, idempotency_key, requested_by,
	next_attempt_at, created_at, updated_at
) `

type scanner interface{ Scan(...any) error }

func scanBackup(row scanner, run *BackupRun) error {
	return row.Scan(
		&run.ID, &run.TriggerType, &run.Status, &run.ScheduledFor, &run.RequestedBy,
		&run.LocalSnapshotID, &run.COSSnapshotID, &run.ManifestSHA256, &run.ByteSize,
		&run.ObjectCount, &run.Attempts, &run.NextAttemptAt, &run.LeaseUntil,
		&run.ErrorMessage, &run.StartedAt, &run.FinishedAt, &run.CreatedAt, &run.UpdatedAt,
	)
}

const verificationColumns = `
	id::text, backup_run_id::text, trigger_type, status, scheduled_for,
	COALESCE(requested_by::text,''), temporary_database, migration_version,
	checked_objects, lease_until, error_message, started_at, finished_at,
	created_at, updated_at`
const verificationReturningColumns = `
	verification.id::text, verification.backup_run_id::text,
	verification.trigger_type, verification.status, verification.scheduled_for,
	COALESCE(verification.requested_by::text,''), verification.temporary_database,
	verification.migration_version, verification.checked_objects,
	verification.lease_until, verification.error_message, verification.started_at,
	verification.finished_at, verification.created_at, verification.updated_at`
const verificationSelect = `SELECT ` + verificationColumns + ` FROM restore_verifications`
const verificationInsert = `INSERT INTO restore_verifications (
	backup_run_id, trigger_type, status, scheduled_for, idempotency_key,
	requested_by, created_at, updated_at
) `

func scanVerification(row scanner, verification *RestoreVerification) error {
	return row.Scan(
		&verification.ID, &verification.BackupRunID, &verification.TriggerType,
		&verification.Status, &verification.ScheduledFor, &verification.RequestedBy,
		&verification.TemporaryDatabase, &verification.MigrationVersion,
		&verification.CheckedObjects, &verification.LeaseUntil, &verification.ErrorMessage,
		&verification.StartedAt, &verification.FinishedAt, &verification.CreatedAt,
		&verification.UpdatedAt,
	)
}

func insertAudit(ctx context.Context, tx pgx.Tx, actorID, action, targetType, targetID string, at time.Time) error {
	details, _ := json.Marshal(map[string]any{})
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_logs (id, actor_id, action, target_type, target_id, details, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
	`, uuid.NewString(), actorID, action, targetType, targetID, details, at); err != nil {
		return fmt.Errorf("记录备份操作审计：%w", err)
	}
	return nil
}

func truncate(value string, maximum int) string {
	if len(value) <= maximum {
		return value
	}
	value = value[:maximum]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func boundedLimit(limit int) int {
	if limit <= 0 {
		return 20
	}
	if limit > 100 {
		return 100
	}
	return limit
}

var _ Repository = (*PostgresRepository)(nil)
