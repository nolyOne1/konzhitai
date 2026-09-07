package agentprotocol

import "time"

type UpgradeAction string
type UpgradeStage string

const (
	UpgradeInstall  UpgradeAction = "install"
	UpgradeRollback UpgradeAction = "rollback"

	StageAccepted     UpgradeStage = "accepted"
	StageDownloading  UpgradeStage = "downloading"
	StageVerifying    UpgradeStage = "verifying"
	StageInstalling   UpgradeStage = "installing"
	StageReconnecting UpgradeStage = "reconnecting"
	StageSucceeded    UpgradeStage = "succeeded"
	StageRollingBack  UpgradeStage = "rolling_back"
	StageRolledBack   UpgradeStage = "rolled_back"
	StageFailed       UpgradeStage = "failed"
)

type UpgradeCommand struct {
	MessageType      string        `json:"message_type"`
	CommandID        string        `json:"command_id"`
	PlanID           string        `json:"plan_id"`
	TargetID         string        `json:"target_id"`
	Action           UpgradeAction `json:"action"`
	SourceVersion    string        `json:"source_version"`
	TargetVersion    string        `json:"target_version"`
	InstallCommandID string        `json:"install_command_id,omitempty"`
	DownloadURL      string        `json:"download_url,omitempty"`
	FileName         string        `json:"file_name,omitempty"`
	ByteSize         int64         `json:"byte_size,omitempty"`
	SHA256           string        `json:"sha256,omitempty"`
	ReconnectTimeout time.Duration `json:"reconnect_timeout"`
}

type UpgradeEvent struct {
	MessageType string       `json:"message_type"`
	CommandID   string       `json:"command_id"`
	TargetID    string       `json:"target_id"`
	Stage       UpgradeStage `json:"stage"`
	OccurredAt  time.Time    `json:"occurred_at"`
	ErrorCode   string       `json:"error_code,omitempty"`
	Message     string       `json:"message,omitempty"`
}

type UpgradeRuntimeState struct {
	CommandID       string       `json:"command_id"`
	TargetID        string       `json:"target_id,omitempty"`
	TargetVersion   string       `json:"target_version,omitempty"`
	RollbackVersion string       `json:"rollback_version,omitempty"`
	Stage           UpgradeStage `json:"stage"`
	UpdatedAt       time.Time    `json:"updated_at"`
	ErrorCode       string       `json:"error_code,omitempty"`
	Message         string       `json:"message,omitempty"`
}
