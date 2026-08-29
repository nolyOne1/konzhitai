package agent

import (
	"context"
	"testing"
)

func TestCollectorReportsConfiguredRuntimes(t *testing.T) {
	collector := NewCollector(fakeStats{snapshot: Stats{
		CPUTotalMilli:    8000,
		CPUUsedMilli:     2500,
		MemoryTotalBytes: 16 << 30,
		MemoryUsedBytes:  4 << 30,
		DiskTotalBytes:   100 << 30,
		DiskFreeBytes:    20 << 30,
		RunningTasks:     2,
	}}, []string{"bash", "python3"}, WithLogSpool(fakeSpoolUsage{used: 80, limit: 100}))

	got, err := collector.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("采集资源快照：%v", err)
	}
	if got.CPUUsedMilli != 2500 || got.MemoryUsedBytes != 4<<30 || got.DiskFreeBytes != 20<<30 {
		t.Fatalf("资源数据未完整上报：%+v", got)
	}
	if got.CPUTotalMilli != 8000 || got.MemoryTotalBytes != 16<<30 || got.DiskTotalBytes != 100<<30 {
		t.Fatalf("资源总容量未完整上报：%+v", got)
	}
	if got.RunningTasks != 2 {
		t.Fatalf("运行任务数应为 2，实际为 %d", got.RunningTasks)
	}
	if len(got.Runtimes) != 2 || got.Runtimes[0] != "bash" || got.Runtimes[1] != "python3" {
		t.Fatalf("运行环境应为 bash、python3，实际为 %#v", got.Runtimes)
	}
	if got.LogSpoolUsedBytes != 80 || got.LogSpoolLimitBytes != 100 {
		t.Fatalf("日志缓冲容量未完整上报：%+v", got)
	}
}

type fakeStats struct {
	snapshot Stats
	err      error
}

func (f fakeStats) Snapshot(context.Context) (Stats, error) {
	return f.snapshot, f.err
}

type fakeSpoolUsage struct{ used, limit int64 }

func (f fakeSpoolUsage) Usage() (int64, int64) { return f.used, f.limit }
