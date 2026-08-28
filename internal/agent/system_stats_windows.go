//go:build windows

package agent

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32                 = windows.NewLazySystemDLL("kernel32.dll")
	globalMemoryStatusExProc = kernel32.NewProc("GlobalMemoryStatusEx")
	getSystemTimesProc       = kernel32.NewProc("GetSystemTimes")
)

type windowsMemoryStatus struct {
	Length                   uint32
	MemoryLoad               uint32
	TotalPhysical            uint64
	AvailablePhysical        uint64
	TotalPageFile            uint64
	AvailablePageFile        uint64
	TotalVirtual             uint64
	AvailableVirtual         uint64
	AvailableExtendedVirtual uint64
}

type SystemStatsSource struct {
	diskPath     string
	runningTasks func() int
	mu           sync.Mutex
	previousCPU  cpuCounters
}

func NewSystemStatsSource(diskPath string, runningTasks func() int) (*SystemStatsSource, error) {
	cpu, err := readWindowsCPU()
	if err != nil {
		return nil, err
	}
	if runningTasks == nil {
		runningTasks = func() int { return 0 }
	}
	return &SystemStatsSource{diskPath: diskPath, runningTasks: runningTasks, previousCPU: cpu}, nil
}

func (s *SystemStatsSource) Snapshot(context.Context) (Stats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cpu, err := readWindowsCPU()
	if err != nil {
		return Stats{}, err
	}
	memoryTotal, memoryUsed, err := readWindowsMemory()
	if err != nil {
		return Stats{}, err
	}
	diskTotal, diskFree, err := readWindowsDisk(s.diskPath)
	if err != nil {
		return Stats{}, err
	}
	cpuTotal := int64(runtime.NumCPU() * 1000)
	stats := Stats{
		CPUTotalMilli:    cpuTotal,
		CPUUsedMilli:     calculateCPUUsedMilli(s.previousCPU, cpu, cpuTotal),
		MemoryTotalBytes: memoryTotal,
		MemoryUsedBytes:  memoryUsed,
		DiskTotalBytes:   diskTotal,
		DiskFreeBytes:    diskFree,
		RunningTasks:     s.runningTasks(),
	}
	s.previousCPU = cpu
	return stats, nil
}

func readWindowsCPU() (cpuCounters, error) {
	var idle windows.Filetime
	var kernel windows.Filetime
	var user windows.Filetime
	result, _, callErr := getSystemTimesProc.Call(
		uintptr(unsafe.Pointer(&idle)),
		uintptr(unsafe.Pointer(&kernel)),
		uintptr(unsafe.Pointer(&user)),
	)
	if result == 0 {
		return cpuCounters{}, fmt.Errorf("读取 Windows CPU 统计：%v", callErr)
	}
	idleValue := filetimeValue(idle)
	return cpuCounters{total: filetimeValue(kernel) + filetimeValue(user), idle: idleValue}, nil
}

func readWindowsMemory() (int64, int64, error) {
	status := windowsMemoryStatus{Length: uint32(unsafe.Sizeof(windowsMemoryStatus{}))}
	result, _, callErr := globalMemoryStatusExProc.Call(uintptr(unsafe.Pointer(&status)))
	if result == 0 {
		return 0, 0, fmt.Errorf("读取 Windows 内存统计：%v", callErr)
	}
	return int64(status.TotalPhysical), int64(status.TotalPhysical - status.AvailablePhysical), nil
}

func readWindowsDisk(path string) (int64, int64, error) {
	directory, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, fmt.Errorf("解析磁盘路径：%w", err)
	}
	var available uint64
	var total uint64
	var free uint64
	if err := windows.GetDiskFreeSpaceEx(directory, &available, &total, &free); err != nil {
		return 0, 0, fmt.Errorf("读取 Windows 磁盘统计：%w", err)
	}
	return int64(total), int64(available), nil
}

func filetimeValue(value windows.Filetime) uint64 {
	return uint64(value.HighDateTime)<<32 | uint64(value.LowDateTime)
}
