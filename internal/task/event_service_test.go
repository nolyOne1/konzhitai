package task

import (
	"context"
	"errors"
	"testing"
	"time"

	"yunling.local/platform/internal/agentprotocol"
)

func TestEventServiceAppliesAgentEventOnceByTokenAndSequence(t *testing.T) {
	store := &memoryRunEventStore{token: "token-1", state: Assigned, seen: map[uint64]agentprotocol.RunEvent{}}
	service := NewEventService(store)
	event := agentprotocol.RunEvent{RunID: "run-1", ExecutionToken: "token-1", Sequence: 1, Type: "started", OccurredAt: time.Now()}

	if err := service.Apply(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if err := service.Apply(context.Background(), event); err != nil {
		t.Fatalf("相同令牌和序号的事件必须幂等：%v", err)
	}
	if store.state != Running || store.appended != 1 {
		t.Fatalf("运行事件只应生效一次：state=%s appended=%d", store.state, store.appended)
	}
}

func TestEventServiceRejectsStaleTokenAndConflictingDuplicate(t *testing.T) {
	store := &memoryRunEventStore{token: "current-token", state: Assigned, seen: map[uint64]agentprotocol.RunEvent{}}
	service := NewEventService(store)
	stale := agentprotocol.RunEvent{RunID: "run-1", ExecutionToken: "old-token", Sequence: 1, Type: "started", OccurredAt: time.Now()}
	if err := service.Apply(context.Background(), stale); !errors.Is(err, ErrExecutionTokenMismatch) {
		t.Fatalf("过期执行令牌必须拒绝，实际 %v", err)
	}

	valid := stale
	valid.ExecutionToken = "current-token"
	if err := service.Apply(context.Background(), valid); err != nil {
		t.Fatal(err)
	}
	conflict := valid
	conflict.Type = "failed"
	if err := service.Apply(context.Background(), conflict); !errors.Is(err, ErrRunEventConflict) {
		t.Fatalf("同序号冲突事件必须拒绝，实际 %v", err)
	}
}

type memoryRunEventStore struct {
	token    string
	state    RunState
	seen     map[uint64]agentprotocol.RunEvent
	appended int
}

func (s *memoryRunEventStore) ApplyRunEvent(_ context.Context, event agentprotocol.RunEvent, state RunState) (bool, error) {
	if event.ExecutionToken != s.token {
		return false, ErrExecutionTokenMismatch
	}
	if previous, exists := s.seen[event.Sequence]; exists {
		if previous.Type != event.Type || previous.ExitCode != event.ExitCode || previous.Message != event.Message {
			return false, ErrRunEventConflict
		}
		return false, nil
	}
	s.seen[event.Sequence] = event
	s.state = state
	s.appended++
	return true, nil
}
