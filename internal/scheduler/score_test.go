package scheduler_test

import (
	"testing"

	"yunling.local/platform/internal/scheduler"
	"yunling.local/platform/internal/task"
)

func TestScoreUsesDocumentedIntegerWeights(t *testing.T) {
	run := task.Run{Resources: task.Resources{CPUMillicores: 1, MemoryBytes: 1, DiskBytes: 1}}
	candidate := scheduler.Candidate{
		ServerID:     "server-a",
		Total:        task.Resources{CPUMillicores: 1000, MemoryBytes: 1000, DiskBytes: 1000},
		Available:    task.Resources{CPUMillicores: 750, MemoryBytes: 500, DiskBytes: 900},
		RunningTasks: 1, MaxConcurrency: 4, ScriptCached: true,
		FairnessScore: 10000, SchedulingWeight: 100,
	}

	if got, want := scheduler.Score(run, candidate), int64(7125); got != want {
		t.Fatalf("评分权重应为内存35%%、CPU25%%、低运行数20%%、缓存15%%、公平性5%%，got=%d want=%d", got, want)
	}
}

func TestRankCandidatesBreaksEqualScoresByServerID(t *testing.T) {
	run := task.Run{}
	candidates := []scheduler.Candidate{{ServerID: "server-b"}, {ServerID: "server-a"}}

	ranked := scheduler.RankCandidates(run, candidates)
	if ranked[0].ServerID != "server-a" || ranked[1].ServerID != "server-b" {
		t.Fatalf("相同分数必须按服务器 ID 稳定排序，实际=%v", []string{ranked[0].ServerID, ranked[1].ServerID})
	}
}
