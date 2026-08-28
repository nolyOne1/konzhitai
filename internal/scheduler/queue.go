package scheduler

import (
	"sort"
	"sync"

	"yunling.local/platform/internal/task"
)

type Queue struct {
	mu    sync.Mutex
	items map[string]task.Run
}

func NewQueue() *Queue {
	return &Queue{items: map[string]task.Run{}}
}

func (q *Queue) Push(run task.Run) {
	if run.ID == "" {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.items[run.ID] = run
}

func (q *Queue) Pop() (task.Run, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 {
		return task.Run{}, false
	}
	items := make([]task.Run, 0, len(q.items))
	for _, item := range q.items {
		items = append(items, item)
	}
	sortRuns(items)
	selected := items[0]
	delete(q.items, selected.ID)
	return selected, true
}

func (q *Queue) Remove(runID string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.items, runID)
}

func (q *Queue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

func sortRuns(items []task.Run) {
	sort.SliceStable(items, func(left, right int) bool {
		if items[left].Priority != items[right].Priority {
			return items[left].Priority > items[right].Priority
		}
		if !items[left].QueuedAt.Equal(items[right].QueuedAt) {
			return items[left].QueuedAt.Before(items[right].QueuedAt)
		}
		return items[left].ID < items[right].ID
	})
}
