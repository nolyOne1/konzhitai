package task

import "time"

type RunState string

const (
	Queued     RunState = "queued"
	Scheduling RunState = "scheduling"
	Assigned   RunState = "assigned"
	Syncing    RunState = "syncing"
	Running    RunState = "running"
	Succeeded  RunState = "succeeded"
	Failed     RunState = "failed"
	TimedOut   RunState = "timed_out"
	Cancelled  RunState = "cancelled"
	Expired    RunState = "expired"
	Unknown    RunState = "unknown"
)

func (s RunState) Terminal() bool {
	switch s {
	case Succeeded, Failed, TimedOut, Cancelled, Expired:
		return true
	default:
		return false
	}
}

type VersionPolicy string

const (
	VersionLatest VersionPolicy = "latest"
	VersionPinned VersionPolicy = "pinned"
)

type TriggerType string

const (
	TriggerManual   TriggerType = "manual"
	TriggerSchedule TriggerType = "schedule"
	TriggerRetry    TriggerType = "retry"
)

type Resources struct {
	CPUMillicores int   `json:"cpuMillicores"`
	MemoryBytes   int64 `json:"memoryBytes"`
	DiskBytes     int64 `json:"diskBytes"`
}

type RetryPolicy struct {
	MaxRetries     int `json:"maxRetries"`
	BackoffSeconds int `json:"backoffSeconds"`
}

type Definition struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	Description     string            `json:"description"`
	ScriptID        string            `json:"scriptId"`
	ScriptName      string            `json:"scriptName"`
	VersionPolicy   VersionPolicy     `json:"versionPolicy"`
	PinnedVersionID string            `json:"pinnedVersionId,omitempty"`
	Parameters      map[string]any    `json:"parameters"`
	SecretRefs      map[string]string `json:"secretRefs"`
	Priority        int               `json:"priority"`
	RequiredLabels  map[string]string `json:"requiredLabels"`
	RequiredRuntime string            `json:"requiredRuntime"`
	Resources       Resources         `json:"resources"`
	MaxConcurrency  int               `json:"maxConcurrency"`
	TimeoutSeconds  int               `json:"timeoutSeconds"`
	MaxWaitSeconds  int               `json:"maxWaitSeconds"`
	RetryPolicy     RetryPolicy       `json:"retryPolicy"`
	Idempotent      bool              `json:"idempotent"`
	Enabled         bool              `json:"enabled"`
	CreatedBy       string            `json:"createdBy,omitempty"`
	CreatedAt       time.Time         `json:"createdAt"`
	UpdatedAt       time.Time         `json:"updatedAt"`
}

type CreateInput struct {
	Name            string            `json:"name"`
	Description     string            `json:"description"`
	ScriptID        string            `json:"scriptId"`
	VersionPolicy   VersionPolicy     `json:"versionPolicy"`
	PinnedVersionID string            `json:"pinnedVersionId"`
	Parameters      map[string]any    `json:"parameters"`
	SecretRefs      map[string]string `json:"secretRefs"`
	Priority        int               `json:"priority"`
	RequiredLabels  map[string]string `json:"requiredLabels"`
	RequiredRuntime string            `json:"requiredRuntime"`
	Resources       Resources         `json:"resources"`
	MaxConcurrency  int               `json:"maxConcurrency"`
	TimeoutSeconds  int               `json:"timeoutSeconds"`
	MaxWaitSeconds  int               `json:"maxWaitSeconds"`
	RetryPolicy     RetryPolicy       `json:"retryPolicy"`
	Idempotent      bool              `json:"idempotent"`
	Enabled         bool              `json:"enabled"`
	CreatedBy       string            `json:"createdBy,omitempty"`
}

type Trigger struct {
	Type         TriggerType    `json:"type"`
	RequestedBy  string         `json:"requestedBy,omitempty"`
	Parameters   map[string]any `json:"parameters,omitempty"`
	ScheduledFor *time.Time     `json:"scheduledFor,omitempty"`
}

type Run struct {
	ID              string            `json:"id"`
	DefinitionID    string            `json:"definitionId"`
	ScriptVersionID string            `json:"scriptVersionId"`
	TriggerType     TriggerType       `json:"triggerType"`
	State           RunState          `json:"state"`
	Parameters      map[string]any    `json:"parameters"`
	RequiredLabels  map[string]string `json:"requiredLabels"`
	RequiredRuntime string            `json:"requiredRuntime"`
	Priority        int               `json:"priority"`
	Resources       Resources         `json:"resources"`
	MaxConcurrency  int               `json:"maxConcurrency"`
	TimeoutSeconds  int               `json:"timeoutSeconds"`
	MaxWaitSeconds  int               `json:"maxWaitSeconds"`
	RetryPolicy     RetryPolicy       `json:"retryPolicy"`
	Idempotent      bool              `json:"idempotent"`
	ScheduledFor    *time.Time        `json:"scheduledFor,omitempty"`
	QueuedAt        time.Time         `json:"queuedAt"`
	CreatedAt       time.Time         `json:"createdAt"`
}

type Schedule struct {
	ID             string     `json:"id"`
	DefinitionID   string     `json:"definitionId"`
	CronExpression string     `json:"cronExpression"`
	Timezone       string     `json:"timezone"`
	Enabled        bool       `json:"enabled"`
	NextRunAt      *time.Time `json:"nextRunAt,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
}

type ScheduleInput struct {
	DefinitionID   string `json:"definitionId"`
	CronExpression string `json:"cronExpression"`
	Timezone       string `json:"timezone"`
	Enabled        bool   `json:"enabled"`
}
