package backup_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"yunling.local/platform/internal/backup"
	"yunling.local/platform/internal/testpostgres"
)

func TestPostgresRepositoryCreatesScheduledBackupOnce(t *testing.T) {
	db := backupDatabase(t)
	repository := backup.NewPostgresRepository(db)
	ctx := context.Background()
	scheduledFor := time.Date(2026, 9, 1, 4, 30, 0, 0, time.UTC)

	first, err := repository.RequestBackup(ctx, "", "", scheduledFor)
	if err != nil {
		t.Fatal(err)
	}
	second, err := repository.RequestBackup(ctx, "", "", scheduledFor)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || first.TriggerType != backup.TriggerScheduled {
		t.Fatalf("同一计划时间必须返回同一备份：first=%+v second=%+v", first, second)
	}
}

func TestPostgresRepositoryClaimsBackupOnceAndTakesOverExpiredLease(t *testing.T) {
	db := backupDatabase(t)
	repository := backup.NewPostgresRepository(db)
	ctx := context.Background()
	now := time.Date(2026, 9, 1, 5, 0, 0, 0, time.UTC)
	if _, err := repository.RequestBackup(ctx, "", "", now); err != nil {
		t.Fatal(err)
	}

	type result struct {
		run backup.BackupRun
		ok  bool
		err error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			run, ok, err := repository.ClaimBackup(ctx, now, 30*time.Minute)
			results <- result{run: run, ok: ok, err: err}
		}()
	}
	claimed := 0
	var first backup.BackupRun
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.ok {
			claimed++
			first = result.run
		}
	}
	if claimed != 1 || first.Status != backup.StatusExporting || first.Attempts != 1 {
		t.Fatalf("同一时刻只能领取一个备份：claimed=%d run=%+v", claimed, first)
	}

	takenOver, ok, err := repository.ClaimBackup(ctx, now.Add(31*time.Minute), 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || takenOver.ID != first.ID || takenOver.Attempts != 2 {
		t.Fatalf("租约过期应接管同一备份：ok=%v run=%+v", ok, takenOver)
	}
}

func TestPostgresRepositoryRejectsInvalidBackupTransition(t *testing.T) {
	db := backupDatabase(t)
	repository := backup.NewPostgresRepository(db)
	ctx := context.Background()
	now := time.Date(2026, 9, 1, 5, 30, 0, 0, time.UTC)
	run, err := repository.RequestBackup(ctx, "", "", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.MarkBackupSucceeded(ctx, run.ID, "cos-snapshot", now); !errors.Is(err, backup.ErrInvalidTransition) {
		t.Fatalf("排队状态不能直接成功，实际错误：%v", err)
	}
}

func TestPostgresRepositoryDegradedBackupRetainsSnapshotAndResumesUpload(t *testing.T) {
	db := backupDatabase(t)
	repository := backup.NewPostgresRepository(db)
	ctx := context.Background()
	now := time.Date(2026, 9, 1, 6, 0, 0, 0, time.UTC)
	if _, err := repository.RequestBackup(ctx, "", "", now); err != nil {
		t.Fatal(err)
	}
	run, ok, err := repository.ClaimBackup(ctx, now, 30*time.Minute)
	if err != nil || !ok {
		t.Fatalf("领取备份：ok=%v err=%v", ok, err)
	}
	result := backup.SnapshotResult{
		SnapshotID:     "local-snapshot",
		ManifestSHA256: strings.Repeat("a", 64),
		ByteSize:       2048,
		ObjectCount:    3,
	}
	if err := repository.MarkLocalSnapshot(ctx, run.ID, result, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := repository.MarkBackupDegraded(ctx, run.ID, strings.Repeat("远", 5000), now.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}

	resumed, ok, err := repository.ClaimBackup(ctx, now.Add(5*time.Minute), 30*time.Minute)
	if err != nil || !ok {
		t.Fatalf("领取降级备份：ok=%v err=%v", ok, err)
	}
	if resumed.Status != backup.StatusUploading || resumed.LocalSnapshotID != "local-snapshot" {
		t.Fatalf("降级备份必须从本机快照续传：%+v", resumed)
	}
	if len(resumed.ErrorMessage) > 4096 {
		t.Fatalf("错误必须有界，实际 %d 字节", len(resumed.ErrorMessage))
	}
}

func TestPostgresRepositoryManualRequestsAreIdempotentAuditedAndListedNewestFirst(t *testing.T) {
	db := backupDatabase(t)
	repository := backup.NewPostgresRepository(db)
	ctx := context.Background()
	actorID := insertBackupUser(t, db)
	now := time.Date(2026, 9, 1, 6, 30, 0, 0, time.UTC)
	key := uuid.NewString()

	first, err := repository.RequestBackup(ctx, actorID, key, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := repository.RequestBackup(ctx, actorID, key, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || first.TriggerType != backup.TriggerManual {
		t.Fatalf("手动请求必须幂等：first=%+v second=%+v", first, second)
	}
	if _, err := repository.RequestBackup(ctx, actorID, uuid.NewString(), now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	items, err := repository.ListBackups(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || !items[0].CreatedAt.After(items[1].CreatedAt) {
		t.Fatalf("备份历史必须按创建时间倒序：%+v", items)
	}
	var audits int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE action='operations.backup.request'`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 2 {
		t.Fatalf("每个新手动备份必须审计一次，实际 %d", audits)
	}
}

func TestPostgresRepositoryVerificationLeaseAndCompletion(t *testing.T) {
	db := backupDatabase(t)
	repository := backup.NewPostgresRepository(db)
	ctx := context.Background()
	now := time.Date(2026, 9, 1, 7, 0, 0, 0, time.UTC)
	actorID := insertBackupUser(t, db)
	backupRun, err := repository.RequestBackup(ctx, actorID, uuid.NewString(), now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		UPDATE backup_runs SET status='succeeded', local_snapshot_id='local',
		cos_snapshot_id='cos', finished_at=$2, updated_at=$2 WHERE id=$1
	`, backupRun.ID, now); err != nil {
		t.Fatal(err)
	}

	verification, err := repository.RequestVerification(ctx, actorID, backupRun.ID, uuid.NewString(), now)
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := repository.ClaimVerification(ctx, now, 30*time.Minute)
	if err != nil || !ok || claimed.ID != verification.ID || claimed.Status != backup.VerificationRestoring {
		t.Fatalf("领取恢复校验失败：ok=%v verification=%+v err=%v", ok, claimed, err)
	}
	if err := repository.CompleteVerification(ctx, backup.VerificationResult{
		VerificationID:   claimed.ID,
		MigrationVersion: "12",
		CheckedObjects:   3,
	}, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	items, err := repository.ListVerifications(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Status != backup.VerificationSucceeded || items[0].MigrationVersion != "12" {
		t.Fatalf("恢复校验完成状态错误：%+v", items)
	}
}

func TestPostgresRepositorySummaryDistinguishesNotStartedAndDegraded(t *testing.T) {
	db := backupDatabase(t)
	repository := backup.NewPostgresRepository(db)
	ctx := context.Background()
	summary, err := repository.Summary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Status != "not_started" || summary.LatestLocalBackup != nil || summary.LatestCOSBackup != nil {
		t.Fatalf("空备份摘要错误：%+v", summary)
	}

	run, err := repository.RequestBackup(ctx, "", "", time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		UPDATE backup_runs SET status='degraded', local_snapshot_id='local-snapshot',
		manifest_sha256=$2, byte_size=1024, object_count=2 WHERE id=$1
	`, run.ID, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.RequestBackup(ctx, "", "", time.Now().UTC().Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	summary, err = repository.Summary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Status != "degraded" || summary.NextBackupAt == nil || summary.LatestLocalBackup == nil || summary.LatestCOSBackup != nil {
		t.Fatalf("降级备份摘要错误：%+v", summary)
	}
}

func backupDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()
	db := testpostgres.Start(t)
	root := testpostgres.RepositoryRoot(t)
	paths, err := filepath.Glob(filepath.Join(root, "migrations", "*.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		testpostgres.ApplyMigration(t, db, filepath.Base(path))
	}
	return db
}

func insertBackupUser(t *testing.T, db *pgxpool.Pool) string {
	t.Helper()
	var id string
	if err := db.QueryRow(context.Background(), `
		INSERT INTO users (email, display_name, password_hash)
		VALUES ($1, '备份管理员', 'test-hash') RETURNING id::text
	`, fmt.Sprintf("backup-%d@example.com", time.Now().UnixNano())).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}
