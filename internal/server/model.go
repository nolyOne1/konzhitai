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
