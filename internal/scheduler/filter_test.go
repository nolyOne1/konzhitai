package scheduler_test

import (
	"testing"

	"yunling.local/platform/internal/scheduler"
	"yunling.local/platform/internal/server"
	"yunling.local/platform/internal/task"
)

func TestFilterRejectsEveryUnschedulableServerCondition(t *testing.T) {
	run := schedulableRun("run-filter", 1000, 2<<30, 4<<30)
	cases := []struct {
		name   string
		mutate func(*server.Snapshot)
	}{
		{name: "服务器离线", mutate: func(item *server.Snapshot) { item.Status = server.StatusOffline }},
		{name: "服务器已停用", mutate: func(item *server.Snapshot) { item.Enabled = false }},
		{name: "服务器正在排空", mutate: func(item *server.Snapshot) { item.Draining = true }},
		{name: "标签不匹配", mutate: func(item *server.Snapshot) { item.Labels["用途"] = "交互" }},
		{name: "运行环境不匹配", mutate: func(item *server.Snapshot) { item.Runtimes = []string{"python3"} }},
		{name: "并发已满", mutate: func(item *server.Snapshot) { item.RunningTasks = item.MaxConcurrency }},
		{name: "脚本版本被隔离", mutate: func(item *server.Snapshot) { item.BlockedScriptVersions[run.ScriptVersionID] = true }},
		{name: "CPU 不足", mutate: func(item *server.Snapshot) { item.CPUAvailableMillicores = 999 }},
		{name: "内存不足", mutate: func(item *server.Snapshot) { item.MemoryAvailableBytes = 2<<30 - 1 }},
		{name: "磁盘不足", mutate: func(item *server.Snapshot) { item.DiskAvailableBytes = 4<<30 - 1 }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			item := schedulableServer("server-a")
			tc.mutate(&item)
			if got := scheduler.Filter(run, []server.Snapshot{item}); len(got) != 0 {
				t.Fatalf("不可调度服务器必须被过滤，实际候选=%+v", got)
			}
		})
	}
}

func TestFilterKeepsCompatibleServerAndMarksCachedScript(t *testing.T) {
	run := schedulableRun("run-filter-ready", 1000, 2<<30, 4<<30)
	item := schedulableServer("server-a")
	item.ReadyScriptVersions[run.ScriptVersionID] = true

	candidates := scheduler.Filter(run, []server.Snapshot{item})
	if len(candidates) != 1 {
		t.Fatalf("兼容服务器应成为候选，实际数量=%d", len(candidates))
	}
	if !candidates[0].ScriptCached {
		t.Fatal("已同步脚本版本应标记为本地缓存命中")
	}
}

func schedulableRun(id string, cpu int, memory, disk int64) task.Run {
	return task.Run{
		ID: id, DefinitionID: "definition-a", ScriptVersionID: "version-a", State: task.Queued,
		RequiredRuntime: "bash", RequiredLabels: map[string]string{"用途": "批处理"},
		Resources:      task.Resources{CPUMillicores: cpu, MemoryBytes: memory, DiskBytes: disk},
		MaxConcurrency: 2,
	}
}

func schedulableServer(id string) server.Snapshot {
	return server.Snapshot{
		ID: id, Status: server.StatusOnline, Enabled: true,
		Labels: map[string]string{"用途": "批处理"}, Runtimes: []string{"bash"},
		MaxConcurrency: 4, RunningTasks: 1,
		CPUTotalMillicores: 8000, CPUAvailableMillicores: 6000,
		MemoryTotalBytes: 16 << 30, MemoryAvailableBytes: 12 << 30,
		DiskTotalBytes: 100 << 30, DiskAvailableBytes: 80 << 30,
		ReadyScriptVersions: map[string]bool{}, BlockedScriptVersions: map[string]bool{},
		SchedulingWeight: 100, FairnessScore: 10000,
	}
}
