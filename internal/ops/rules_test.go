package ops

import (
	"context"
	"testing"
	"time"

	"yunling.local/platform/internal/alert"
)

func TestRuleEngineAppliesOfflineQueueAndFailureThresholds(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	lastSeen := now.Add(-119 * time.Second)
	repository := newMemoryRuleRepository()
	repository.snapshot.Servers = []ServerObservation{{ID: "server-1", Name: "京东云执行节点", Enabled: true, Draining: true, LastSeenAt: &lastSeen}}
	repository.snapshot.Runs = []RunObservation{
		{ID: "queued-1", TaskName: "排队任务", State: "queued", QueuedAt: now.Add(-599 * time.Second)},
		{ID: "failed-1", TaskName: "失败任务", State: "failed", QueuedAt: now.Add(-time.Hour)},
	}
	sink := &memoryAlertSink{}
	engine := NewRuleEngine(repository, sink, func() time.Time { return now })

	if err := engine.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sink.raiseCount("agent_offline", "server-1") != 0 || sink.raiseCount("queue_timeout", "queued-1") != 0 {
		t.Fatalf("阈值前不得告警：%+v", sink.raised)
	}
	if sink.raiseCount("task_failed", "failed-1") != 1 {
		t.Fatalf("失败运行必须告警一次：%+v", sink.raised)
	}
	if err := engine.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sink.raiseCount("task_failed", "failed-1") != 1 {
		t.Fatal("同一失败运行不得重复 Raise")
	}

	now = now.Add(time.Second)
	if err := engine.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sink.raiseCount("agent_offline", "server-1") != 1 || sink.raiseCount("queue_timeout", "queued-1") != 1 {
		t.Fatalf("达到阈值必须告警：%+v", sink.raised)
	}

	repository.snapshot.Servers[0].LastSeenAt = &now
	repository.snapshot.Runs[0].State = "assigned"
	if err := engine.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sink.resolveCount("agent_offline", "server-1") != 1 || sink.resolveCount("queue_timeout", "queued-1") != 1 {
		t.Fatalf("恢复心跳和离开队列后必须恢复告警：%+v", sink.resolved)
	}

	repository.snapshot.Servers[0].Enabled = false
	repository.snapshot.Servers[0].LastSeenAt = nil
	if err := engine.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sink.raiseCount("agent_offline", "server-1") != 1 {
		t.Fatal("已停用服务器不得新增离线告警")
	}
}

func TestRuleEngineRequiresTwoFreshResourceSamplesAndHysteresisRecovery(t *testing.T) {
	now := time.Date(2026, 8, 31, 13, 0, 0, 0, time.UTC)
	lastSeen := now
	repository := newMemoryRuleRepository()
	repository.snapshot.Servers = []ServerObservation{{ID: "server-1", Name: "执行节点", Enabled: true, LastSeenAt: &lastSeen}}
	sink := &memoryAlertSink{}
	engine := NewRuleEngine(repository, sink, func() time.Time { return now })

	setResourceSample(repository, now, 9, 14)
	if err := engine.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sink.raised) != 0 {
		t.Fatal("单个低资源样本不得告警")
	}
	now = now.Add(15 * time.Second)
	lastSeen = now
	repository.snapshot.Servers[0].LastSeenAt = &lastSeen
	setResourceSample(repository, now, 9, 14)
	if err := engine.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sink.raiseCount("memory_low", "server-1") != 1 || sink.raiseCount("disk_low", "server-1") != 1 {
		t.Fatalf("连续两个低资源样本必须告警：%+v", sink.raised)
	}

	now = now.Add(15 * time.Second)
	lastSeen = now
	repository.snapshot.Servers[0].LastSeenAt = &lastSeen
	setResourceSample(repository, now, 16, 21)
	if err := engine.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sink.resolved) != 0 {
		t.Fatal("单个恢复样本不得恢复资源告警")
	}
	now = now.Add(15 * time.Second)
	lastSeen = now
	repository.snapshot.Servers[0].LastSeenAt = &lastSeen
	setResourceSample(repository, now, 16, 21)
	if err := engine.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sink.resolveCount("memory_low", "server-1") != 1 || sink.resolveCount("disk_low", "server-1") != 1 {
		t.Fatalf("连续两个恢复样本必须恢复：%+v", sink.resolved)
	}

	now = now.Add(3 * time.Minute)
	lastSeen = now
	repository.snapshot.Servers[0].LastSeenAt = &lastSeen
	repository.snapshot.Servers[0].Samples = []ResourceSample{
		{MemoryTotalBytes: 0, DiskTotalBytes: 0, CollectedAt: now},
		{MemoryTotalBytes: 100, MemoryAvailableBytes: 1, DiskTotalBytes: 100, DiskAvailableBytes: 1, CollectedAt: now.Add(-3 * time.Minute)},
	}
	before := len(sink.raised)
	if err := engine.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sink.raised) != before {
		t.Fatal("总量为零或过期快照不得参与资源规则")
	}
}

func setResourceSample(repository *memoryRuleRepository, at time.Time, memoryPercent, diskPercent int64) {
	repository.snapshot.Servers[0].Samples = []ResourceSample{{
		MemoryTotalBytes: 100, MemoryAvailableBytes: memoryPercent,
		DiskTotalBytes: 100, DiskAvailableBytes: diskPercent, CollectedAt: at,
	}}
}

type memoryRuleRepository struct {
	snapshot RuleSnapshot
	states   map[RuleKey]RuleState
}

func newMemoryRuleRepository() *memoryRuleRepository {
	return &memoryRuleRepository{states: map[RuleKey]RuleState{}}
}

func (r *memoryRuleRepository) Snapshot(context.Context, time.Time) (RuleSnapshot, error) {
	return r.snapshot, nil
}

func (r *memoryRuleRepository) Apply(_ context.Context, evaluations []Evaluation) ([]Transition, error) {
	for _, evaluation := range evaluations {
		state := r.states[evaluation.Key]
		if evaluation.SampleBased && !evaluation.EvaluatedAt.After(state.LastEvaluatedAt) {
			continue
		}
		state.LastEvaluatedAt = evaluation.EvaluatedAt
		state.LastValue = evaluation.Value
		switch {
		case evaluation.Bad:
			state.ConsecutiveBad++
			state.ConsecutiveGood = 0
			if state.ConsecutiveBad >= evaluation.RequiredConsecutive {
				state.DesiredActive = true
			}
		case evaluation.Good:
			state.ConsecutiveGood++
			state.ConsecutiveBad = 0
			if state.ConsecutiveGood >= evaluation.RequiredConsecutive {
				state.DesiredActive = false
			}
		default:
			state.ConsecutiveBad, state.ConsecutiveGood = 0, 0
		}
		r.states[evaluation.Key] = state
	}
	transitions := []Transition{}
	for _, evaluation := range evaluations {
		state := r.states[evaluation.Key]
		if state.DesiredActive != state.Active {
			transitions = append(transitions, Transition{Evaluation: evaluation, DesiredActive: state.DesiredActive})
		}
	}
	return deduplicateTransitions(transitions), nil
}

func (r *memoryRuleRepository) MarkApplied(_ context.Context, key RuleKey, desired bool) error {
	state := r.states[key]
	if state.DesiredActive == desired {
		state.Active = desired
		r.states[key] = state
	}
	return nil
}

type memoryAlertSink struct {
	raised   []alert.Event
	resolved []RuleKey
}

func (s *memoryAlertSink) Raise(_ context.Context, event alert.Event) error {
	s.raised = append(s.raised, event)
	return nil
}

func (s *memoryAlertSink) Resolve(_ context.Context, sourceType, sourceID, code string) error {
	s.resolved = append(s.resolved, RuleKey{Code: code, SourceType: sourceType, SourceID: sourceID})
	return nil
}

func (s *memoryAlertSink) raiseCount(code, sourceID string) int {
	count := 0
	for _, event := range s.raised {
		if event.Code == code && event.ResourceID == sourceID {
			count++
		}
	}
	return count
}

func (s *memoryAlertSink) resolveCount(code, sourceID string) int {
	count := 0
	for _, key := range s.resolved {
		if key.Code == code && key.SourceID == sourceID {
			count++
		}
	}
	return count
}
