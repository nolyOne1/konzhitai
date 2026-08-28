//go:build linux

package agent

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sync"

	"golang.org/x/sys/unix"
)

type SystemStatsSource struct {
	diskPath     string
	runningTasks func() int
	mu           sync.Mutex
	previousCPU  cpuCounters
}

func NewSystemStatsSource(diskPath string, runningTasks func() int) (*SystemStatsSource, error) {
	contents, err := os.ReadFile("/proc/stat")
	if err != nil {
		return nil, fmt.Errorf("读取 CPU 统计：%w", err)
	}
	cpu, err := parseProcCPU(contents)
	if err != nil {
		return nil, fmt.Errorf("解析 CPU 统计：%w", err)
	}
	if runningTasks == nil {
		runningTasks = func() int { return 0 }
	}
	return &SystemStatsSource{diskPath: diskPath, runningTasks: runningTasks, previousCPU: cpu}, nil
}

func (s *SystemStatsSource) Snapshot(context.Context) (Stats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cpuContents, err := os.ReadFile("/proc/stat")
	if err != nil {
		return Stats{}, fmt.Errorf("读取 CPU 统计：%w", err)
	}
	cpu, err := parseProcCPU(cpuContents)
	if err != nil {
		return Stats{}, fmt.Errorf("解析 CPU 统计：%w", err)
	}
	memoryContents, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return Stats{}, fmt.Errorf("读取内存统计：%w", err)
	}
	memoryTotal, memoryUsed, err := parseProcMeminfo(memoryContents)
	if err != nil {
		return Stats{}, fmt.Errorf("解析内存统计：%w", err)
	}
	var disk unix.Statfs_t
	if err := unix.Statfs(s.diskPath, &disk); err != nil {
		return Stats{}, fmt.Errorf("读取磁盘统计：%w", err)
	}
	cpuTotal := int64(runtime.NumCPU() * 1000)
	stats := Stats{
		CPUTotalMilli:    cpuTotal,
		CPUUsedMilli:     calculateCPUUsedMilli(s.previousCPU, cpu, cpuTotal),
		MemoryTotalBytes: memoryTotal,
		MemoryUsedBytes:  memoryUsed,
		DiskTotalBytes:   int64(disk.Blocks) * int64(disk.Bsize),
		DiskFreeBytes:    int64(disk.Bavail) * int64(disk.Bsize),
		RunningTasks:     s.runningTasks(),
	}
	s.previousCPU = cpu
	return stats, nil
}
