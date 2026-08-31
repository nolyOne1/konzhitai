package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"
)

type BackupStateStore interface {
	ClaimBackup(context.Context, time.Time, time.Duration) (BackupRun, bool, error)
	MarkLocalSnapshot(context.Context, string, SnapshotResult, time.Time) error
	MarkBackupFailed(context.Context, string, string, time.Time) error
	ListBackups(context.Context, int) ([]BackupRun, error)
}

type Service struct {
	store          BackupStateStore
	exporter       DataExporter
	snapshotter    LocalSnapshotter
	now            func() time.Time
	availableBytes func(string) (int64, error)
	lease          time.Duration
	root           string
}

func NewService(store BackupStateStore, exporter DataExporter, snapshotter LocalSnapshotter, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	root := "."
	if concrete, ok := exporter.(*Exporter); ok && concrete.configuration.Root != "" {
		root = concrete.configuration.Root
	}
	return &Service{
		store: store, exporter: exporter, snapshotter: snapshotter, now: now,
		availableBytes: availableDiskBytes, lease: 30 * time.Minute, root: root,
	}
}

func (s *Service) RunBackup(ctx context.Context) error {
	if s == nil || s.store == nil || s.exporter == nil || s.snapshotter == nil {
		return ErrUnavailable
	}
	now := s.now().UTC()
	run, ok, err := s.store.ClaimBackup(ctx, now, s.lease)
	if err != nil || !ok {
		return err
	}
	if run.LocalSnapshotID != "" {
		return nil
	}
	latestBytes, err := s.latestSuccessfulBytes(ctx)
	if err != nil {
		return s.fail(ctx, run.ID, "读取历史备份大小失败", err)
	}
	available, err := s.availableBytes(s.root)
	if err != nil {
		return s.fail(ctx, run.ID, "检查备份空间失败", err)
	}
	if !HasEnoughSpace(available, latestBytes) {
		return s.fail(ctx, run.ID, "备份暂存空间不足", errors.New("可用空间低于安全阈值"))
	}
	exported, err := s.exporter.Export(ctx, run)
	if err != nil {
		return s.fail(ctx, run.ID, "导出备份数据失败", err)
	}
	snapshotID, err := s.snapshotter.SnapshotLocal(ctx, exported.Root, run.ID)
	if err != nil {
		return s.fail(ctx, run.ID, "生成本机快照失败", err)
	}
	result := SnapshotResult{
		SnapshotID: snapshotID, ManifestSHA256: exported.ManifestSHA256,
		ByteSize: exported.ByteSize, ObjectCount: exported.ObjectCount,
	}
	if err := s.store.MarkLocalSnapshot(ctx, run.ID, result, s.now().UTC()); err != nil {
		return err
	}
	if err := os.RemoveAll(exported.Root); err != nil {
		return fmt.Errorf("清理备份暂存目录：%w", err)
	}
	return nil
}

func (s *Service) latestSuccessfulBytes(ctx context.Context) (int64, error) {
	runs, err := s.store.ListBackups(ctx, 100)
	if err != nil {
		return 0, err
	}
	for _, run := range runs {
		if run.Status == StatusSucceeded && run.ByteSize > 0 {
			return run.ByteSize, nil
		}
	}
	return 0, nil
}

func (s *Service) fail(ctx context.Context, runID, safeMessage string, cause error) error {
	if markErr := s.store.MarkBackupFailed(ctx, runID, safeMessage, s.now().UTC()); markErr != nil {
		return fmt.Errorf("%s：%v；记录失败状态：%w", safeMessage, cause, markErr)
	}
	return fmt.Errorf("%s：%w", safeMessage, cause)
}
