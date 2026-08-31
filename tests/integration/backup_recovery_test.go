package integration_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"yunling.local/platform/internal/backup"
	"yunling.local/platform/internal/testpostgres"
)

func TestBackupRecoveryFlowPersistsDegradedRetryAndIsolatedVerification(t *testing.T) {
	db := backupRecoveryDatabase(t)
	repository := backup.NewPostgresRepository(db)
	ctx := context.Background()
	clock := time.Date(2026, 9, 1, 0, 30, 0, 0, time.UTC)
	run, err := repository.RequestBackup(ctx, "", "", clock)
	if err != nil {
		t.Fatal(err)
	}
	exporter := &integrationBackupExporter{root: t.TempDir()}
	snapshotter := &integrationSnapshotter{}
	remote := &integrationRemote{failures: 1}
	retention := &integrationRetention{}
	service := backup.NewService(repository, exporter, snapshotter, func() time.Time { return clock }).
		WithRemote(remote, retention, nil)

	if err := service.RunBackup(ctx); err == nil {
		t.Fatal("COS 第一次不可用时必须返回可观测错误")
	}
	degraded := onlyBackup(t, repository)
	if degraded.Status != backup.StatusDegraded || degraded.LocalSnapshotID == "" || degraded.COSSnapshotID != "" {
		t.Fatalf("本机快照成功后必须持久化为降级状态：%+v", degraded)
	}

	clock = clock.Add(16 * time.Minute)
	if err := service.RunBackup(ctx); err != nil {
		t.Fatalf("COS 恢复后续传同一快照：%v", err)
	}
	succeeded := onlyBackup(t, repository)
	if succeeded.ID != run.ID || succeeded.Status != backup.StatusSucceeded || succeeded.Attempts != 2 {
		t.Fatalf("降级续传必须完成同一条备份记录：%+v", succeeded)
	}
	if exporter.calls != 1 || snapshotter.calls != 1 || remote.calls != 2 || retention.calls != 1 {
		t.Fatalf("续传不得重新导出或创建本机快照：export=%d snapshot=%d remote=%d retention=%d",
			exporter.calls, snapshotter.calls, remote.calls, retention.calls)
	}

	verification, err := repository.RequestVerification(ctx, "", succeeded.ID, "", clock)
	if err != nil {
		t.Fatal(err)
	}
	service.WithVerifier(repository, integrationVerifier{})
	if err := service.RunVerification(ctx); err != nil {
		t.Fatalf("隔离恢复校验：%v", err)
	}
	verifications, err := repository.ListVerifications(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(verifications) != 1 || verifications[0].ID != verification.ID ||
		verifications[0].Status != backup.VerificationSucceeded || verifications[0].MigrationVersion != "12" {
		t.Fatalf("恢复校验结果未完整持久化：%+v", verifications)
	}
	summary, err := repository.Summary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Status != "healthy" || summary.LatestCOSBackup == nil || summary.LatestVerification == nil {
		t.Fatalf("成功续传与校验后摘要必须健康：%+v", summary)
	}
}

func TestBackupWorkerTakesOverExpiredLeaseWithoutCreatingDuplicateRun(t *testing.T) {
	db := backupRecoveryDatabase(t)
	repository := backup.NewPostgresRepository(db)
	ctx := context.Background()
	startedAt := time.Date(2026, 9, 1, 6, 30, 0, 0, time.UTC)
	requested, err := repository.RequestBackup(ctx, "", "", startedAt)
	if err != nil {
		t.Fatal(err)
	}
	crashed, claimed, err := repository.ClaimBackup(ctx, startedAt, 30*time.Minute)
	if err != nil || !claimed || crashed.ID != requested.ID {
		t.Fatalf("模拟进程退出前领取租约失败：claimed=%v run=%+v err=%v", claimed, crashed, err)
	}

	clock := startedAt.Add(31 * time.Minute)
	exporter := &integrationBackupExporter{root: t.TempDir()}
	snapshotter := &integrationSnapshotter{}
	remote := &integrationRemote{}
	service := backup.NewService(repository, exporter, snapshotter, func() time.Time { return clock }).
		WithRemote(remote, &integrationRetention{}, nil)
	if err := service.RunBackup(ctx); err != nil {
		t.Fatalf("接管过期租约：%v", err)
	}
	completed := onlyBackup(t, repository)
	if completed.ID != requested.ID || completed.Status != backup.StatusSucceeded || completed.Attempts != 2 {
		t.Fatalf("接管必须复用原记录且只增加尝试次数：%+v", completed)
	}
	if exporter.calls != 1 || snapshotter.calls != 1 || remote.calls != 1 {
		t.Fatalf("接管后只应执行一次完整管线：export=%d snapshot=%d remote=%d", exporter.calls, snapshotter.calls, remote.calls)
	}
}

type integrationBackupExporter struct {
	root  string
	calls int
}

func (e *integrationBackupExporter) Export(_ context.Context, run backup.BackupRun) (backup.ExportResult, error) {
	e.calls++
	staging := filepath.Join(e.root, run.ID)
	if err := os.MkdirAll(staging, 0o700); err != nil {
		return backup.ExportResult{}, err
	}
	if err := os.WriteFile(filepath.Join(staging, "manifest.json"), []byte("{}"), 0o600); err != nil {
		return backup.ExportResult{}, err
	}
	return backup.ExportResult{Root: staging, ManifestSHA256: strings.Repeat("a", 64), ByteSize: 1024, ObjectCount: 1}, nil
}

type integrationSnapshotter struct{ calls int }

func (s *integrationSnapshotter) SnapshotLocal(_ context.Context, _ string, runID string) (string, error) {
	s.calls++
	return "local-" + runID, nil
}

type integrationRemote struct {
	calls    int
	failures int
}

func (r *integrationRemote) CopyToCOS(_ context.Context, _ string, runID string) (string, error) {
	r.calls++
	if r.calls <= r.failures {
		return "", errors.New("COS 暂时不可用")
	}
	return "cos-" + runID, nil
}

type integrationRetention struct{ calls int }

func (r *integrationRetention) Apply(context.Context, backup.BackupRun) error {
	r.calls++
	return nil
}

type integrationVerifier struct{}

func (integrationVerifier) Verify(_ context.Context, verification backup.RestoreVerification, run backup.BackupRun) (backup.VerificationResult, error) {
	if run.Status != backup.StatusSucceeded || run.COSSnapshotID == "" {
		return backup.VerificationResult{}, errors.New("只能校验已同步到 COS 的备份")
	}
	return backup.VerificationResult{
		VerificationID: verification.ID, TemporaryDatabase: "yunling_verify_" + strings.Repeat("a", 32),
		MigrationVersion: "12", CheckedObjects: run.ObjectCount,
	}, nil
}

func onlyBackup(t *testing.T, repository *backup.PostgresRepository) backup.BackupRun {
	t.Helper()
	runs, err := repository.ListBackups(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("预期只有一条备份记录，实际 %d：%+v", len(runs), runs)
	}
	return runs[0]
}

func backupRecoveryDatabase(t *testing.T) *pgxpool.Pool {
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
