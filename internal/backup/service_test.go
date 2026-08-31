package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

type localServiceStore struct {
	run    BackupRun
	marked SnapshotResult
	failed string
}

func (s *localServiceStore) ClaimBackup(context.Context, time.Time, time.Duration) (BackupRun, bool, error) {
	return s.run, true, nil
}
func (s *localServiceStore) MarkLocalSnapshot(_ context.Context, _ string, result SnapshotResult, _ time.Time) error {
	s.marked = result
	return nil
}
func (s *localServiceStore) MarkBackupFailed(_ context.Context, _ string, message string, _ time.Time) error {
	s.failed = message
	return nil
}
func (s *localServiceStore) ListBackups(context.Context, int) ([]BackupRun, error) { return nil, nil }

type localServiceExporter struct{ result ExportResult }

func (e localServiceExporter) Export(context.Context, BackupRun) (ExportResult, error) {
	return e.result, nil
}

type localServiceSnapshotter struct{ sawRoot string }

func (s *localServiceSnapshotter) SnapshotLocal(_ context.Context, root, _ string) (string, error) {
	s.sawRoot = root
	return "local-snapshot", nil
}

func TestServicePersistsLocalSnapshotBeforeDeletingStaging(t *testing.T) {
	root := t.TempDir()
	staging := filepath.Join(root, "staging")
	if err := os.MkdirAll(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	store := &localServiceStore{run: BackupRun{ID: uuid.NewString(), Status: StatusExporting}}
	snapshotter := &localServiceSnapshotter{}
	service := NewService(store, localServiceExporter{result: ExportResult{
		Root: staging, ManifestSHA256: "manifest-hash", ByteSize: 123, ObjectCount: 4,
	}}, snapshotter, func() time.Time { return time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC) })
	service.availableBytes = func(string) (int64, error) { return 10 * 1024 * 1024 * 1024, nil }

	if err := service.RunBackup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.marked.SnapshotID != "local-snapshot" || store.marked.ManifestSHA256 != "manifest-hash" {
		t.Fatalf("本机快照未持久化：%+v", store.marked)
	}
	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Fatalf("持久化后应清理暂存目录：%v", err)
	}
}
