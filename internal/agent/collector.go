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
	logSpool LogSpoolUsage
}

type LogSpoolUsage interface {
	Usage() (usedBytes, maxBytes int64)
}

type CollectorOption func(*Collector)

func WithLogSpool(spool LogSpoolUsage) CollectorOption {
	return func(collector *Collector) { collector.logSpool = spool }
}

func NewCollector(source StatsSource, runtimes []string, options ...CollectorOption) *Collector {
	collector := &Collector{
		source:   source,
		runtimes: append([]string(nil), runtimes...),
	}
	for _, option := range options {
		option(collector)
	}
	return collector
}

func (c *Collector) Snapshot(ctx context.Context) (agentprotocol.Heartbeat, error) {
	stats, err := c.source.Snapshot(ctx)
	if err != nil {
		return agentprotocol.Heartbeat{}, err
	}
	heartbeat := agentprotocol.Heartbeat{
		CPUTotalMilli:    stats.CPUTotalMilli,
		CPUUsedMilli:     stats.CPUUsedMilli,
		MemoryTotalBytes: stats.MemoryTotalBytes,
		MemoryUsedBytes:  stats.MemoryUsedBytes,
		DiskTotalBytes:   stats.DiskTotalBytes,
		DiskFreeBytes:    stats.DiskFreeBytes,
		RunningTasks:     stats.RunningTasks,
		Runtimes:         append([]string(nil), c.runtimes...),
	}
	if c.logSpool != nil {
		heartbeat.LogSpoolUsedBytes, heartbeat.LogSpoolLimitBytes = c.logSpool.Usage()
	}
	return heartbeat, nil
}
