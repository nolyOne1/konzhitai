package agentupgrade

import (
	"errors"
	"time"
)

type PlanStatus string
type TargetStatus string

const (
	PlanPending   PlanStatus = "pending"
	PlanRunning   PlanStatus = "running"
	PlanPaused    PlanStatus = "paused"
	PlanSucceeded PlanStatus = "succeeded"
	PlanCancelled PlanStatus = "cancelled"

	TargetWaiting            TargetStatus = "waiting"
	TargetDraining           TargetStatus = "draining"
	TargetDownloading        TargetStatus = "downloading"
	TargetVerifying          TargetStatus = "verifying"
	TargetInstalling         TargetStatus = "installing"
	TargetReconnecting       TargetStatus = "reconnecting"
	TargetHealthChecking     TargetStatus = "health_checking"
	TargetSucceeded          TargetStatus = "succeeded"
	TargetRollingBack        TargetStatus = "rolling_back"
	TargetRolledBack         TargetStatus = "rolled_back"
	TargetManualIntervention TargetStatus = "manual_intervention"
	TargetCancelled          TargetStatus = "cancelled"
)

var (
	ErrInvalidPlan         = errors.New("代理升级计划内容无效")
	ErrActivePlanExists    = errors.New("已有活动中的代理升级计划")
	ErrServerIneligible    = errors.New("服务器当前不符合升级条件")
	ErrUpgradeUnsupported  = errors.New("代理版本或服务器不支持自动升级")
	ErrArtifactUnavailable = errors.New("服务器架构没有可用安装包")
	ErrNoUpgradeNeeded     = errors.New("服务器已经运行目标版本")
	ErrReleaseNotFound     = errors.New("代理版本不存在")
	ErrPlanNotFound        = errors.New("代理升级计划不存在")
	ErrTargetNotFound      = errors.New("代理升级目标不存在")
	ErrInvalidTransition   = errors.New("代理升级状态不允许此操作")
)

type CreatePlanInput struct {
	TargetReleaseID         string   `json:"target_release_id"`
	ServerIDs               []string `json:"server_ids"`
	BatchSize               int      `json:"batch_size"`
	DrainTimeoutSeconds     int      `json:"drain_timeout_seconds"`
	ReconnectTimeoutSeconds int      `json:"reconnect_timeout_seconds"`
	VerificationSeconds     int      `json:"verification_seconds"`
	CreatedBy               string   `json:"-"`
}

type Plan struct {
	ID                      string     `json:"id"`
	TargetReleaseID         string     `json:"target_release_id"`
	TargetVersion           string     `json:"target_version"`
	Status                  PlanStatus `json:"status"`
	FirstBatchSize          int        `json:"first_batch_size"`
	BatchSize               int        `json:"batch_size"`
	DrainTimeoutSeconds     int        `json:"drain_timeout_seconds"`
	ReconnectTimeoutSeconds int        `json:"reconnect_timeout_seconds"`
	VerificationSeconds     int        `json:"verification_seconds"`
	CurrentBatch            int        `json:"current_batch"`
	CreatedBy               string     `json:"created_by"`
	PauseReason             string     `json:"pause_reason"`
	CreatedAt               time.Time  `json:"created_at"`
	StartedAt               *time.Time `json:"started_at,omitempty"`
	FinishedAt              *time.Time `json:"finished_at,omitempty"`
	Targets                 []Target   `json:"targets"`
}

type Target struct {
	ID             string       `json:"id"`
	PlanID         string       `json:"plan_id"`
	ServerID       string       `json:"server_id"`
	BatchNumber    int          `json:"batch_number"`
	SourceVersion  string       `json:"source_version"`
	TargetVersion  string       `json:"target_version"`
	SourceDraining bool         `json:"source_draining"`
	Status         TargetStatus `json:"status"`
	Attempts       int          `json:"attempts"`
	CommandID      string       `json:"command_id"`
	ErrorCode      string       `json:"error_code"`
	ErrorMessage   string       `json:"error_message"`
	StartedAt      *time.Time   `json:"started_at,omitempty"`
	UpdatedAt      time.Time    `json:"updated_at"`
	FinishedAt     *time.Time   `json:"finished_at,omitempty"`
}

type ReleaseInfo struct {
	ID           string
	Version      string
	Status       string
	Capabilities []string
	Artifacts    []ArtifactInfo
}

type ArtifactInfo struct {
	OS, Arch, FileName, SHA256, DownloadURL string
	ByteSize                                int64
}

type ServerRuntime struct {
	Status       string
	Enabled      bool
	Draining     bool
	RunningTasks int
	AgentOS      string
	AgentArch    string
}

type ServerInfo struct {
	ID, Status, AgentVersion, AgentOS, AgentArch string
	Enabled, Draining                            bool
	Capabilities                                 []string
}
