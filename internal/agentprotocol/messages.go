package agentprotocol

import "time"

type Heartbeat struct {
	ServerID           string    `json:"server_id"`
	Sequence           uint64    `json:"sequence"`
	SentAt             time.Time `json:"sent_at"`
	CPUTotalMilli      int64     `json:"cpu_total_milli"`
	CPUUsedMilli       int64     `json:"cpu_used_milli"`
	MemoryTotalBytes   int64     `json:"memory_total_bytes"`
	MemoryUsedBytes    int64     `json:"memory_used_bytes"`
	DiskTotalBytes     int64     `json:"disk_total_bytes"`
	DiskFreeBytes      int64     `json:"disk_free_bytes"`
	RunningTasks       int       `json:"running_tasks"`
	LogSpoolUsedBytes  int64     `json:"log_spool_used_bytes"`
	LogSpoolLimitBytes int64     `json:"log_spool_limit_bytes"`
	Runtimes           []string  `json:"runtimes"`
	AgentVersion       string    `json:"agent_version"`
}
