package agent

import (
	"context"

	"yunling.local/platform/internal/agentprotocol"
)

type Stats struct {
	CPUTotalMilli    int64
	CPUUsedMilli     int64
	MemoryTotalBytes int64
	MemoryUsedBytes  int64
	DiskTotalBytes   int64
	DiskFreeBytes    int64
	RunningTasks     int
}

type StatsSource interface {
	Snapshot(ctx context.Context) (Stats, error)
}

type Collector struct {
	source   StatsSource
	runtimes []string
}

func NewCollector(source StatsSource, runtimes []string) *Collector {
	return &Collector{
		source:   source,
		runtimes: append([]string(nil), runtimes...),
	}
}

func (c *Collector) Snapshot(ctx context.Context) (agentprotocol.Heartbeat, error) {
	stats, err := c.source.Snapshot(ctx)
	if err != nil {
		return agentprotocol.Heartbeat{}, err
	}
	return agentprotocol.Heartbeat{
		CPUTotalMilli:    stats.CPUTotalMilli,
		CPUUsedMilli:     stats.CPUUsedMilli,
		MemoryTotalBytes: stats.MemoryTotalBytes,
		MemoryUsedBytes:  stats.MemoryUsedBytes,
		DiskTotalBytes:   stats.DiskTotalBytes,
		DiskFreeBytes:    stats.DiskFreeBytes,
		RunningTasks:     stats.RunningTasks,
		Runtimes:         append([]string(nil), c.runtimes...),
	}, nil
}
