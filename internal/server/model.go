package server

import "time"

type Status string

const (
	StatusPending     Status = "pending"
	StatusOnline      Status = "online"
	StatusOffline     Status = "offline"
	StatusDraining    Status = "draining"
	StatusQuarantined Status = "quarantined"
)

type Server struct {
	ID            string
	Name          string
	CloudProvider string
	Region        string
	Status        Status
	Labels        map[string]string
	Runtimes      []string
	AgentVersion  string
	LastSeenAt    *time.Time
}

type ResourceSnapshot struct {
	ServerID             string
	CPUUsagePercent      float64
	MemoryAvailableBytes int64
	DiskAvailableBytes   int64
	RunningTasks         int
	CollectedAt          time.Time
}

// Snapshot 是调度器做一次确定性决策所需的服务器状态快照。
type Snapshot struct {
	ID                     string
	Status                 Status
	Enabled                bool
	Draining               bool
	Labels                 map[string]string
	Runtimes               []string
	MaxConcurrency         int
	RunningTasks           int
	CPUTotalMillicores     int
	CPUAvailableMillicores int
	MemoryTotalBytes       int64
	MemoryAvailableBytes   int64
	DiskTotalBytes         int64
	DiskAvailableBytes     int64
	ReadyScriptVersions    map[string]bool
	BlockedScriptVersions  map[string]bool
	SchedulingWeight       int
	FairnessScore          int64
}
