package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"yunling.local/platform/internal/alert"
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
func (s *localServiceStore) MarkBackupSucceeded(context.Context, string, string, time.Time) error {
	return nil
}
func (s *localServiceStore) MarkBackupDegraded(context.Context, string, string, time.Time) error {
	return nil
}
func (s *localServiceStore) ListBackups(context.Context, int) ([]BackupRun, error) { return nil, nil }

type localServiceExporter struct{ result ExportResult }

func (e localServiceExporter) Export(context.Context, BackupRun) (ExportResult, error) {
	return e.result, nil
}

type resumeServiceStore struct {
	run       BackupRun
	degraded  bool
	succeeded bool
}

func (s *resumeServiceStore) ClaimBackup(context.Context, time.Time, time.Duration) (BackupRun, bool, error) {
	return s.run, true, nil
}
func (s *resumeServiceStore) MarkLocalSnapshot(_ context.Context, _ string, result SnapshotResult, _ time.Time) error {
	s.run.LocalSnapshotID = result.SnapshotID
	s.run.Status = StatusUploading
	return nil
}
func (s *resumeServiceStore) MarkBackupFailed(context.Context, string, string, time.Time) error {
	return nil
}
func (s *resumeServiceStore) MarkBackupDegraded(_ context.Context, _ string, _ string, _ time.Time) error {
	s.degraded = true
	s.run.Status = StatusUploading
	return nil
}
func (s *resumeServiceStore) MarkBackupSucceeded(_ context.Context, _ string, cosID string, _ time.Time) error {
	s.succeeded = true
	s.run.Status = StatusSucceeded
	s.run.COSSnapshotID = cosID
	return nil
}
func (s *resumeServiceStore) ListBackups(context.Context, int) ([]BackupRun, error) { return nil, nil }

type countingExporter struct {
	count  int
	result ExportResult
}

func (e *countingExporter) Export(context.Context, BackupRun) (ExportResult, error) {
	e.count++
	return e.result, nil
}

type countingSnapshotter struct{ count int }

func (s *countingSnapshotter) SnapshotLocal(context.Context, string, string) (string, error) {
	s.count++
	return "local-snapshot", nil
}

type flakyRemote struct{ count int }

func (r *flakyRemote) CopyToCOS(context.Context, string, string) (string, error) {
	r.count++
	if r.count == 1 {
		return "", errors.New("temporary COS error with sensitive details")
	}
	return "cos-snapshot", nil
}

type noOpRetention struct{ count int }

func (r *noOpRetention) Apply(context.Context, BackupRun) error { r.count++; return nil }

type recordingAlerts struct {
	raised   []alert.Event
	resolved []string
}

func (a *recordingAlerts) Raise(_ context.Context, event alert.Event) error {
	a.raised = append(a.raised, event)
	return nil
}
func (a *recordingAlerts) Resolve(_ context.Context, _, _, code string) error {
	a.resolved = append(a.resolved, code)
	return nil
}

func TestServiceResumesDegradedCOSCopyWithoutExportingAgain(t *testing.T) {
	root := t.TempDir()
	staging := filepath.Join(root, "staging")
	if err := os.MkdirAll(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	store := &resumeServiceStore{run: BackupRun{ID: uuid.NewString(), Status: StatusExporting}}
	exporter := &countingExporter{result: ExportResult{
		Root: staging, ManifestSHA256: "manifest", ByteSize: 100, ObjectCount: 2,
	}}
	snapshotter := &countingSnapshotter{}
	remote := &flakyRemote{}
	retention := &noOpRetention{}
	alerts := &recordingAlerts{}
	service := NewService(store, exporter, snapshotter, time.Now).WithRemote(remote, retention, alerts)
	service.availableBytes = func(string) (int64, error) { return 10 * 1024 * 1024 * 1024, nil }

	if err := service.RunBackup(context.Background()); err == nil || !store.degraded {
		t.Fatalf("首次 COS 失败必须进入降级状态：degraded=%v err=%v", store.degraded, err)
	}
	if err := service.RunBackup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if exporter.count != 1 || snapshotter.count != 1 || remote.count != 2 {
		t.Fatalf("降级续传不得重新导出：export=%d snapshot=%d copy=%d", exporter.count, snapshotter.count, remote.count)
	}
	if !store.succeeded || retention.count != 1 {
		t.Fatalf("续传成功后必须成功并清理：store=%+v retention=%d", store, retention.count)
	}
	if len(alerts.raised) == 0 || alerts.raised[0].Code != "backup_cos_degraded" {
		t.Fatalf("COS 降级必须告警：%+v", alerts.raised)
	}
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
