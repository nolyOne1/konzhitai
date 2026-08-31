package backup

import (
	"context"
	"errors"
	"time"
)

const (
	TriggerScheduled = "scheduled"
	TriggerManual    = "manual"

	StatusQueued       = "queued"
	StatusExporting    = "exporting"
	StatusSnapshotting = "snapshotting"
	StatusUploading    = "uploading"
	StatusSucceeded    = "succeeded"
	StatusDegraded     = "degraded"
	StatusFailed       = "failed"

	VerificationQueued    = "queued"
	VerificationRestoring = "restoring"
	VerificationChecking  = "checking"
	VerificationSucceeded = "succeeded"
	VerificationFailed    = "failed"
)

var (
	ErrUnavailable       = errors.New("备份服务尚未配置")
	ErrInvalidRequest    = errors.New("备份请求无效")
	ErrInvalidTransition = errors.New("备份状态转换无效")
	ErrNotFound          = errors.New("备份记录不存在")
)

type BackupRun struct {
	ID              string     `json:"id"`
	TriggerType     string     `json:"triggerType"`
	Status          string     `json:"status"`
	ScheduledFor    *time.Time `json:"scheduledFor,omitempty"`
	RequestedBy     string     `json:"requestedBy,omitempty"`
	LocalSnapshotID string     `json:"localSnapshotId,omitempty"`
	COSSnapshotID   string     `json:"cosSnapshotId,omitempty"`
	ManifestSHA256  string     `json:"manifestSha256,omitempty"`
	ByteSize        int64      `json:"byteSize"`
	ObjectCount     int64      `json:"objectCount"`
	Attempts        int        `json:"attempts"`
	NextAttemptAt   time.Time  `json:"nextAttemptAt"`
	LeaseUntil      *time.Time `json:"leaseUntil,omitempty"`
	ErrorMessage    string     `json:"errorMessage,omitempty"`
	StartedAt       *time.Time `json:"startedAt,omitempty"`
	FinishedAt      *time.Time `json:"finishedAt,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

type RestoreVerification struct {
	ID                string     `json:"id"`
	BackupRunID       string     `json:"backupRunId"`
	TriggerType       string     `json:"triggerType"`
	Status            string     `json:"status"`
	ScheduledFor      *time.Time `json:"scheduledFor,omitempty"`
	RequestedBy       string     `json:"requestedBy,omitempty"`
	TemporaryDatabase string     `json:"temporaryDatabase,omitempty"`
	MigrationVersion  string     `json:"migrationVersion,omitempty"`
	CheckedObjects    int64      `json:"checkedObjects"`
	LeaseUntil        *time.Time `json:"leaseUntil,omitempty"`
	ErrorMessage      string     `json:"errorMessage,omitempty"`
	StartedAt         *time.Time `json:"startedAt,omitempty"`
	FinishedAt        *time.Time `json:"finishedAt,omitempty"`
	CreatedAt         time.Time  `json:"createdAt"`
	UpdatedAt         time.Time  `json:"updatedAt"`
}

type SnapshotResult struct {
	SnapshotID     string
	ManifestSHA256 string
	ByteSize       int64
	ObjectCount    int64
}

type VerificationResult struct {
	VerificationID    string
	TemporaryDatabase string
	MigrationVersion  string
	CheckedObjects    int64
	ErrorMessage      string
}

type Summary struct {
	Status             string               `json:"status"`
	NextBackupAt       *time.Time           `json:"nextBackupAt"`
	LatestLocalBackup  *BackupRun           `json:"latestLocalBackup"`
	LatestCOSBackup    *BackupRun           `json:"latestCOSBackup"`
	LatestVerification *RestoreVerification `json:"latestVerification"`
}

type Repository interface {
	EnsureSchedules(context.Context, time.Time) error
	RequestBackup(context.Context, string, string, time.Time) (BackupRun, error)
	ClaimBackup(context.Context, time.Time, time.Duration) (BackupRun, bool, error)
	MarkLocalSnapshot(context.Context, string, SnapshotResult, time.Time) error
	MarkBackupSucceeded(context.Context, string, string, time.Time) error
	MarkBackupDegraded(context.Context, string, string, time.Time) error
	MarkBackupFailed(context.Context, string, string, time.Time) error
	RequestVerification(context.Context, string, string, string, time.Time) (RestoreVerification, error)
	ClaimVerification(context.Context, time.Time, time.Duration) (RestoreVerification, bool, error)
	CompleteVerification(context.Context, VerificationResult, time.Time) error
	ListBackups(context.Context, int) ([]BackupRun, error)
	ListVerifications(context.Context, int) ([]RestoreVerification, error)
}
