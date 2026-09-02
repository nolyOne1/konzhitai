package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"yunling.local/platform/internal/alert"
)

type BackupStateStore interface {
	ClaimBackup(context.Context, time.Time, time.Duration) (BackupRun, bool, error)
	MarkLocalSnapshot(context.Context, string, SnapshotResult, time.Time) error
	MarkBackupSucceeded(context.Context, string, string, time.Time) error
	MarkBackupDegraded(context.Context, string, string, time.Time) error
	MarkBackupFailed(context.Context, string, string, time.Time) error
	ListBackups(context.Context, int) ([]BackupRun, error)
}

type AlertSink interface {
	Raise(context.Context, alert.Event) error
	Resolve(context.Context, string, string, string) error
}

type VerificationStateStore interface {
	ClaimVerification(context.Context, time.Time, time.Duration) (RestoreVerification, bool, error)
	CompleteVerification(context.Context, VerificationResult, time.Time) error
}

type ScheduleStore interface {
	EnsureSchedules(context.Context, time.Time) error
}

type Service struct {
	store             BackupStateStore
	exporter          DataExporter
	snapshotter       LocalSnapshotter
	now               func() time.Time
	availableBytes    func(string) (int64, error)
	lease             time.Duration
	root              string
	remote            RemoteSnapshotter
	retention         RetentionPolicy
	alerts            AlertSink
	verificationStore VerificationStateStore
	verifier          BackupVerifier
}

func (s *Service) WithRemote(remote RemoteSnapshotter, retention RetentionPolicy, alerts AlertSink) *Service {
	if s != nil {
		s.remote = remote
		s.retention = retention
		s.alerts = alerts
	}
	return s
}

func (s *Service) WithVerifier(store VerificationStateStore, verifier BackupVerifier) *Service {
	if s != nil {
		s.verificationStore = store
		s.verifier = verifier
	}
	return s
}

func (s *Service) EnsureSchedules(ctx context.Context, now time.Time) error {
	scheduler, ok := s.store.(ScheduleStore)
	if !ok {
		return ErrUnavailable
	}
	return scheduler.EnsureSchedules(ctx, now)
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
	if run.LocalSnapshotID == "" {
		latestBytes, err := s.latestSuccessfulBytes(ctx)
		if err != nil {
			return s.fail(ctx, run.ID, "读取历史备份大小失败", err)
		}
		available, err := s.availableBytes(s.root)
		if err != nil {
			return s.fail(ctx, run.ID, "检查备份空间失败", err)
		}
		if !HasEnoughSpace(available, latestBytes) {
			s.raise(ctx, "backup_space_insufficient", alert.SeverityCritical, "备份空间不足", "专用备份目录可用空间低于安全阈值")
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
		run.LocalSnapshotID = snapshotID
		run.ManifestSHA256 = exported.ManifestSHA256
		run.ByteSize = exported.ByteSize
		run.ObjectCount = exported.ObjectCount
		run.Status = StatusUploading
		if err := os.RemoveAll(exported.Root); err != nil {
			return fmt.Errorf("清理备份暂存目录：%w", err)
		}
	}
	if s.remote == nil {
		return nil
	}
	cosSnapshotID, err := s.remote.CopyToCOS(ctx, run.LocalSnapshotID, run.ID)
	if err != nil {
		retryAt := s.now().UTC().Add(15 * time.Minute)
		if markErr := s.store.MarkBackupDegraded(ctx, run.ID, "COS 快照复制失败", retryAt); markErr != nil {
			return fmt.Errorf("记录 COS 降级状态：%w", markErr)
		}
		s.raise(ctx, "backup_cos_degraded", alert.SeverityWarning, "COS 备份暂时不可用", "本机加密快照已保留，将自动续传到 COS")
		return errors.New("COS 快照复制失败，已进入持久化重试")
	}
	if err := s.store.MarkBackupSucceeded(ctx, run.ID, cosSnapshotID, s.now().UTC()); err != nil {
		return err
	}
	run.COSSnapshotID = cosSnapshotID
	run.Status = StatusSucceeded
	s.resolve(ctx, "backup_cos_degraded")
	s.resolve(ctx, "backup_failed")
	s.resolve(ctx, "backup_space_insufficient")
	if s.retention != nil {
		if err := s.retention.Apply(ctx, run); err != nil {
			s.raise(ctx, "backup_retention_failed", alert.SeverityWarning, "备份保留清理失败", "新备份已经成功，旧快照清理将在后续重试")
			return nil
		}
		s.resolve(ctx, "backup_retention_failed")
	}
	return nil
}

func (s *Service) RunVerification(ctx context.Context) error {
	if s == nil || s.verificationStore == nil || s.verifier == nil {
		return ErrUnavailable
	}
	now := s.now().UTC()
	verification, ok, err := s.verificationStore.ClaimVerification(ctx, now, s.lease)
	if err != nil || !ok {
		return err
	}
	backups, err := s.store.ListBackups(ctx, 100)
	if err != nil {
		return s.completeVerificationFailure(ctx, verification, "恢复校验读取备份失败")
	}
	var selected BackupRun
	for _, run := range backups {
		if run.ID == verification.BackupRunID {
			selected = run
			break
		}
	}
	if selected.ID == "" {
		return s.completeVerificationFailure(ctx, verification, "恢复校验对应备份不存在")
	}
	result, verifyErr := s.verifier.Verify(ctx, verification, selected)
	if verifyErr != nil {
		if result.ErrorMessage == "" {
			result.ErrorMessage = "恢复校验失败"
		}
		if err := s.verificationStore.CompleteVerification(ctx, result, s.now().UTC()); err != nil {
			return err
		}
		s.raise(ctx, "backup_verification_failed", alert.SeverityCritical, "备份恢复校验失败", result.ErrorMessage)
		return errors.New("备份恢复校验失败")
	}
	if err := s.verificationStore.CompleteVerification(ctx, result, s.now().UTC()); err != nil {
		return err
	}
	s.resolve(ctx, "backup_verification_failed")
	s.raise(ctx, "backup_verification_succeeded", alert.SeverityInfo, "备份恢复校验成功", "COS 快照已通过隔离恢复与完整性检查")
	return nil
}

func (s *Service) completeVerificationFailure(ctx context.Context, verification RestoreVerification, message string) error {
	result := VerificationResult{VerificationID: verification.ID, ErrorMessage: message}
	if err := s.verificationStore.CompleteVerification(ctx, result, s.now().UTC()); err != nil {
		return err
	}
	s.raise(ctx, "backup_verification_failed", alert.SeverityCritical, "备份恢复校验失败", message)
	return errors.New(message)
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
	s.raise(ctx, "backup_failed", alert.SeverityCritical, "自动备份失败", safeMessage)
	return fmt.Errorf("%s：%w", safeMessage, cause)
}

func (s *Service) raise(ctx context.Context, code string, severity alert.Severity, title, message string) {
	if s.alerts != nil {
		_ = s.alerts.Raise(ctx, alert.Event{
			ResourceType: "system", ResourceID: "backup", Code: code,
			Severity: severity, Title: title, Message: message,
		})
	}
}

func (s *Service) resolve(ctx context.Context, code string) {
	if s.alerts != nil {
		_ = s.alerts.Resolve(ctx, "system", "backup", code)
	}
}
