package scheduler_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"yunling.local/platform/internal/alert"
	"yunling.local/platform/internal/scheduler"
	"yunling.local/platform/internal/server"
	"yunling.local/platform/internal/task"
)

func TestScheduleOneKeepsRunQueuedWhenNoServerFits(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	run := schedulableRun("run-too-large", 4000, 8<<30, 10<<30)
	run.QueuedAt, run.MaxWaitSeconds = now.Add(-time.Minute), 3600
	runs := newMemoryRuns(run)
	item := schedulableServer("server-small")
	item.CPUAvailableMillicores, item.MemoryAvailableBytes = 2000, 4<<30
	svc := scheduler.NewService(runs, staticServers{items: []server.Snapshot{item}}, newMemoryLeases(task.Resources{}), func() time.Time { return now })

	outcome, err := svc.ScheduleOne(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("资源不足应继续排队而不是报错：%v", err)
	}
	if outcome != scheduler.OutcomeQueued {
		t.Fatalf("资源不足的结果应为 queued，实际=%s", outcome)
	}
	if got := runs.mustGet(run.ID); got.State != task.Queued {
		t.Fatalf("资源不足必须保持 queued，实际=%s", got.State)
	}
	if len(runs.assignments) != 0 {
		t.Fatalf("资源不足不得创建分配，实际=%+v", runs.assignments)
	}
}

func TestResourceReleasedWakesHighestPriorityWaitingRun(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	low := schedulableRun("run-low", 2000, 2<<30, 1<<30)
	low.Priority, low.QueuedAt, low.MaxWaitSeconds = 10, now.Add(-2*time.Minute), 3600
	high := schedulableRun("run-high", 2000, 2<<30, 1<<30)
	high.Priority, high.QueuedAt, high.MaxWaitSeconds = 50, now.Add(-time.Minute), 3600
	runs := newMemoryRuns(low, high)
	item := schedulableServer("server-a")
	item.CPUAvailableMillicores, item.MemoryAvailableBytes, item.DiskAvailableBytes = 2000, 2<<30, 1<<30
	leases := newMemoryLeases(task.Resources{CPUMillicores: 2000, MemoryBytes: 2 << 30, DiskBytes: 1 << 30})
	svc := scheduler.NewService(runs, staticServers{items: []server.Snapshot{item}}, leases, func() time.Time { return now })

	if err := svc.HandleEvent(context.Background(), scheduler.Event{Type: scheduler.ResourceReleased}); err != nil {
		t.Fatalf("资源释放事件调度失败：%v", err)
	}
	if len(runs.assignments) != 1 || runs.assignments[0].RunID != high.ID {
		t.Fatalf("应优先分配高优先级任务，实际=%+v", runs.assignments)
	}
	if got := runs.mustGet(low.ID); got.State != task.Queued {
		t.Fatalf("低优先级任务应继续排队，实际=%s", got.State)
	}
}

func TestScheduleOneExpiresRunPastMaximumWait(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	run := schedulableRun("run-expired", 1000, 1<<30, 1<<30)
	run.QueuedAt, run.MaxWaitSeconds = now.Add(-time.Hour-time.Second), 3600
	runs := newMemoryRuns(run)
	alerts := &schedulerAlertRecorder{}
	svc := scheduler.NewService(runs, staticServers{items: []server.Snapshot{schedulableServer("server-a")}}, newMemoryLeases(task.Resources{}), func() time.Time { return now }, scheduler.WithAlertSink(alerts))

	outcome, err := svc.ScheduleOne(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("过期排队任务处理失败：%v", err)
	}
	if outcome != scheduler.OutcomeExpired || runs.mustGet(run.ID).State != task.Expired {
		t.Fatalf("超过最大等待必须进入 expired，outcome=%s state=%s", outcome, runs.mustGet(run.ID).State)
	}
	if len(alerts.events) != 1 || alerts.events[0].Code != "task_queue_timeout" || alerts.events[0].ResourceID != run.ID {
		t.Fatalf("排队超过最大等待时间必须生成告警：%+v", alerts.events)
	}
}

type schedulerAlertRecorder struct{ events []alert.Event }

func (r *schedulerAlertRecorder) Raise(_ context.Context, event alert.Event) error {
	r.events = append(r.events, event)
	return nil
}

type memoryRuns struct {
	mu          sync.Mutex
	items       map[string]task.Run
	assignments []scheduler.Assignment
}

func newMemoryRuns(items ...task.Run) *memoryRuns {
	result := &memoryRuns{items: map[string]task.Run{}}
	for _, item := range items {
		result.items[item.ID] = item
	}
	return result
}

func (r *memoryRuns) Get(_ context.Context, id string) (task.Run, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, ok := r.items[id]
	if !ok {
		return task.Run{}, errors.New("任务运行不存在")
	}
	return item, nil
}

func (r *memoryRuns) ListQueued(_ context.Context) ([]task.Run, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := []task.Run{}
	for _, item := range r.items {
		if item.State == task.Queued {
			result = append(result, item)
		}
	}
	return result, nil
}

func (r *memoryRuns) CountActive(_ context.Context, definitionID string) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for _, item := range r.items {
		if item.DefinitionID == definitionID && (item.State == task.Assigned || item.State == task.Syncing || item.State == task.Running) {
			count++
		}
	}
	return count, nil
}

func (r *memoryRuns) Assign(_ context.Context, assignment scheduler.Assignment) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, ok := r.items[assignment.RunID]
	if !ok || item.State != task.Queued {
		return false, nil
	}
	item.State = task.Assigned
	r.items[item.ID] = item
	r.assignments = append(r.assignments, assignment)
	return true, nil
}

func (r *memoryRuns) Expire(_ context.Context, runID string, _ time.Time) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, ok := r.items[runID]
	if !ok || item.State != task.Queued {
		return false, nil
	}
	item.State = task.Expired
	r.items[runID] = item
	return true, nil
}

func (r *memoryRuns) mustGet(id string) task.Run {
	item, ok := r.items[id]
	if !ok {
		panic("missing run " + id)
	}
	return item
}

type staticServers struct{ items []server.Snapshot }

func (s staticServers) Snapshots(context.Context, task.Run) ([]server.Snapshot, error) {
	return s.items, nil
}

type memoryLeases struct {
	mu        sync.Mutex
	available task.Resources
}

func newMemoryLeases(available task.Resources) *memoryLeases {
	return &memoryLeases{available: available}
}

func (s *memoryLeases) TryReserve(_ context.Context, request scheduler.LeaseRequest) (scheduler.Lease, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.available.CPUMillicores < request.Required.CPUMillicores || s.available.MemoryBytes < request.Required.MemoryBytes || s.available.DiskBytes < request.Required.DiskBytes {
		return scheduler.Lease{}, false, nil
	}
	s.available.CPUMillicores -= request.Required.CPUMillicores
	s.available.MemoryBytes -= request.Required.MemoryBytes
	s.available.DiskBytes -= request.Required.DiskBytes
	return scheduler.Lease{ID: "lease-" + request.RunID, RunID: request.RunID, ServerID: request.ServerID, Resources: request.Required, ExpiresAt: request.Now.Add(request.TTL)}, true, nil
}

func (s *memoryLeases) Release(_ context.Context, lease scheduler.Lease) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.available.CPUMillicores += lease.Resources.CPUMillicores
	s.available.MemoryBytes += lease.Resources.MemoryBytes
	s.available.DiskBytes += lease.Resources.DiskBytes
	return nil
}
