package agentprotocol

import "time"

type ResourceLimits struct {
	CPUMillicores int   `json:"cpu_millicores"`
	MemoryBytes   int64 `json:"memory_bytes"`
	DiskBytes     int64 `json:"disk_bytes"`
	TasksMax      int   `json:"tasks_max"`
}

type Assignment struct {
	RunID           string            `json:"run_id"`
	ExecutionToken  string            `json:"execution_token"`
	ScriptVersionID string            `json:"script_version_id"`
	Runtime         string            `json:"runtime"`
	ScriptPath      string            `json:"script_path"`
	Arguments       []string          `json:"arguments"`
	Environment     map[string]string `json:"environment"`
	Resources       ResourceLimits    `json:"resources"`
	Timeout         time.Duration     `json:"timeout"`
}

type CancelCommand struct {
	RunID          string `json:"run_id"`
	ExecutionToken string `json:"execution_token"`
}

type ExecutionCommandType string

const (
	CommandAssign ExecutionCommandType = "assignment"
	CommandCancel ExecutionCommandType = "cancel"
)

type ExecutionCommand struct {
	Type         ExecutionCommandType `json:"type"`
	Assignment   *Assignment          `json:"assignment,omitempty"`
	Cancellation *CancelCommand       `json:"cancellation,omitempty"`
}

type RunEvent struct {
	RunID          string    `json:"run_id"`
	ExecutionToken string    `json:"execution_token"`
	Sequence       uint64    `json:"sequence"`
	Type           string    `json:"type"`
	OccurredAt     time.Time `json:"occurred_at"`
	ExitCode       int       `json:"exit_code,omitempty"`
	Message        string    `json:"message,omitempty"`
}
