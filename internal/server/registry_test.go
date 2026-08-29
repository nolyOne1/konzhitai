package server

import (
	"context"
	"testing"
	"time"

	"yunling.local/platform/internal/agentprotocol"
	"yunling.local/platform/internal/alert"
)

func TestRegistryIgnoresOlderHeartbeat(t *testing.T) {
	repository := newMemoryServerRepository()
	receivedAt := time.Date(2026, 8, 28, 11, 0, 0, 0, time.UTC)
	registry := NewRegistry(repository, func() time.Time { return receivedAt })
	ctx := context.Background()

	if err := registry.AcceptHeartbeat(ctx, heartbeat(10, 3200)); err != nil {
		t.Fatalf("接收新心跳：%v", err)
	}
	if err := registry.AcceptHeartbeat(ctx, heartbeat(9, 900)); err != nil {
		t.Fatalf("接收乱序心跳：%v", err)
	}

	if repository.snapshot.CPUUsedMilli != 3200 {
		t.Fatalf("旧心跳不得覆盖新快照，CPU 应为 3200，实际为 %d", repository.snapshot.CPUUsedMilli)
	}
	if repository.sequence != 10 {
		t.Fatalf("最新心跳序号应保持为 10，实际为 %d", repository.sequence)
	}
	if !repository.receivedAt.Equal(receivedAt) {
		t.Fatalf("在线时间必须使用中央接收时间，实际为 %s", repository.receivedAt)
	}
}

func TestRegistryMarksServerOfflineAfterFifteenSeconds(t *testing.T) {
	repository := newMemoryServerRepository()
	repository.offlineIDs = []string{"server-1"}
	publisher := &memoryEventPublisher{}
	now := time.Date(2026, 8, 28, 11, 0, 20, 0, time.UTC)
	registry := NewRegistry(
		repository,
		func() time.Time { return now },
		WithEventPublisher(publisher),
	)

	if err := registry.ReconcileOffline(context.Background()); err != nil {
		t.Fatalf("执行离线对账：%v", err)
	}

	wantCutoff := time.Date(2026, 8, 28, 11, 0, 5, 0, time.UTC)
	if !repository.offlineCutoff.Equal(wantCutoff) {
		t.Fatalf("离线判定时间应为当前时间减 15 秒，实际为 %s", repository.offlineCutoff)
	}
	if len(publisher.events) != 1 {
		t.Fatalf("应发布一个离线事件，实际为 %d", len(publisher.events))
	}
	if publisher.events[0].Type != "server.offline" || publisher.events[0].ServerID != "server-1" {
		t.Fatalf("离线事件内容错误：%+v", publisher.events[0])
	}
}

func TestRegistryRaisesAlertWhenLogSpoolNearLimit(t *testing.T) {
	repository := newMemoryServerRepository()
	sink := &memoryAlertSink{}
	registry := NewRegistry(repository, time.Now, WithAlertSink(sink))
	heartbeat := heartbeat(1, 1000)
	heartbeat.LogSpoolUsedBytes = 800
	heartbeat.LogSpoolLimitBytes = 1000

	if err := registry.AcceptHeartbeat(context.Background(), heartbeat); err != nil {
		t.Fatalf("接收日志缓冲告警心跳：%v", err)
	}
	if len(sink.events) != 1 {
		t.Fatalf("达到 80%% 时应生成一个告警，实际为 %d", len(sink.events))
	}
	event := sink.events[0]
	if event.ResourceType != "server" || event.ResourceID != "server-1" || event.Code != "log_spool_near_limit" {
		t.Fatalf("日志缓冲告警内容错误：%+v", event)
	}
}

func heartbeat(sequence uint64, cpuUsedMilli int64) agentprotocol.Heartbeat {
	return agentprotocol.Heartbeat{
		ServerID:        "server-1",
		Sequence:        sequence,
		SentAt:          time.Date(2026, 8, 28, 10, 59, 0, 0, time.UTC),
		CPUUsedMilli:    cpuUsedMilli,
		MemoryUsedBytes: 4 << 30,
		DiskFreeBytes:   20 << 30,
		RunningTasks:    1,
		Runtimes:        []string{"bash", "python3"},
		AgentVersion:    "0.1.0",
	}
}

type memoryServerRepository struct {
	found         bool
	sequence      uint64
	snapshot      agentprotocol.Heartbeat
	receivedAt    time.Time
	offlineIDs    []string
	offlineCutoff time.Time
	saved         chan struct{}
}

func newMemoryServerRepository() *memoryServerRepository {
	return &memoryServerRepository{}
}

func (r *memoryServerRepository) LatestHeartbeatSequence(context.Context, string) (uint64, bool, error) {
	return r.sequence, r.found, nil
}

func (r *memoryServerRepository) SaveHeartbeat(
	_ context.Context,
	heartbeat agentprotocol.Heartbeat,
	receivedAt time.Time,
) (bool, error) {
	r.found = true
	r.sequence = heartbeat.Sequence
	r.snapshot = heartbeat
	r.receivedAt = receivedAt
	if r.saved != nil {
		select {
		case r.saved <- struct{}{}:
		default:
		}
	}
	return true, nil
}

func (r *memoryServerRepository) MarkOfflineBefore(_ context.Context, cutoff time.Time) ([]string, error) {
	r.offlineCutoff = cutoff
	return append([]string(nil), r.offlineIDs...), nil
}

type memoryEventPublisher struct {
	events []Event
}

type memoryAlertSink struct{ events []alert.Event }

func (s *memoryAlertSink) Raise(_ context.Context, event alert.Event) error {
	s.events = append(s.events, event)
	return nil
}

func (p *memoryEventPublisher) Publish(_ context.Context, event Event) error {
	p.events = append(p.events, event)
	return nil
}
