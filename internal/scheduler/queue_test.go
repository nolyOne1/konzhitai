package scheduler_test

import (
	"testing"
	"time"

	"yunling.local/platform/internal/scheduler"
	"yunling.local/platform/internal/task"
)

func TestQueuePopsPriorityThenQueueTimeThenRunID(t *testing.T) {
	queue := scheduler.NewQueue()
	base := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	for _, run := range []task.Run{
		{ID: "run-low", Priority: 10, QueuedAt: base},
		{ID: "run-high-late", Priority: 80, QueuedAt: base.Add(time.Minute)},
		{ID: "run-high-b", Priority: 80, QueuedAt: base},
		{ID: "run-high-a", Priority: 80, QueuedAt: base},
	} {
		queue.Push(run)
	}

	want := []string{"run-high-a", "run-high-b", "run-high-late", "run-low"}
	for index, wantID := range want {
		got, ok := queue.Pop()
		if !ok || got.ID != wantID {
			t.Fatalf("第 %d 个出队任务 got=%s ok=%v want=%s", index, got.ID, ok, wantID)
		}
	}
}

func TestQueueDeduplicatesSameRun(t *testing.T) {
	queue := scheduler.NewQueue()
	queue.Push(task.Run{ID: "run-a", Priority: 10})
	queue.Push(task.Run{ID: "run-a", Priority: 90})
	if queue.Len() != 1 {
		t.Fatalf("同一运行实例只能排队一次，实际=%d", queue.Len())
	}
	run, _ := queue.Pop()
	if run.Priority != 90 {
		t.Fatalf("重复入队应更新为最新快照，priority=%d", run.Priority)
	}
}
