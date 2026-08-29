package server

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"yunling.local/platform/internal/agentprotocol"
	"yunling.local/platform/internal/alert"
)

var ErrInvalidHeartbeat = errors.New("心跳内容无效")

type HeartbeatRepository interface {
	LatestHeartbeatSequence(ctx context.Context, serverID string) (sequence uint64, found bool, err error)
	SaveHeartbeat(ctx context.Context, heartbeat agentprotocol.Heartbeat, receivedAt time.Time) (accepted bool, err error)
	MarkOfflineBefore(ctx context.Context, cutoff time.Time) ([]string, error)
}

type Clock func() time.Time

type Event struct {
	Type       string
	ServerID   string
	OccurredAt time.Time
}

type EventPublisher interface {
	Publish(ctx context.Context, event Event) error
}

type RegistryOption func(*Registry)

func WithEventPublisher(publisher EventPublisher) RegistryOption {
	return func(registry *Registry) {
		registry.publisher = publisher
	}
}

type registryAlertSink interface {
	Raise(context.Context, alert.Event) error
}

func WithAlertSink(sink registryAlertSink) RegistryOption {
	return func(registry *Registry) { registry.alerts = sink }
}

type Registry struct {
	repository HeartbeatRepository
	clock      Clock
	publisher  EventPublisher
	alerts     registryAlertSink

	mu     sync.Mutex
	latest map[string]uint64
	loaded map[string]bool
}

func NewRegistry(repository HeartbeatRepository, clock Clock, options ...RegistryOption) *Registry {
	registry := &Registry{
		repository: repository,
		clock:      clock,
		publisher:  discardEventPublisher{},
		latest:     make(map[string]uint64),
		loaded:     make(map[string]bool),
	}
	for _, option := range options {
		option(registry)
	}
	return registry
}

func (r *Registry) AcceptHeartbeat(ctx context.Context, heartbeat agentprotocol.Heartbeat) error {
	if heartbeat.ServerID == "" {
		return ErrInvalidHeartbeat
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.loaded[heartbeat.ServerID] {
		sequence, found, err := r.repository.LatestHeartbeatSequence(ctx, heartbeat.ServerID)
		if err != nil {
			return err
		}
		if found {
			r.latest[heartbeat.ServerID] = sequence
		}
		r.loaded[heartbeat.ServerID] = true
	}
	if latest, found := r.latest[heartbeat.ServerID]; found && heartbeat.Sequence <= latest {
		return nil
	}

	accepted, err := r.repository.SaveHeartbeat(ctx, heartbeat, r.clock().UTC())
	if err != nil {
		return err
	}
	if accepted {
		r.latest[heartbeat.ServerID] = heartbeat.Sequence
		alertThreshold := heartbeat.LogSpoolLimitBytes - heartbeat.LogSpoolLimitBytes/5
		if r.alerts != nil && heartbeat.LogSpoolLimitBytes > 0 &&
			heartbeat.LogSpoolUsedBytes >= alertThreshold {
			percentage := int(float64(heartbeat.LogSpoolUsedBytes) / float64(heartbeat.LogSpoolLimitBytes) * 100)
			if err := r.alerts.Raise(ctx, alert.Event{
				ResourceType: "server", ResourceID: heartbeat.ServerID, Code: "log_spool_near_limit",
				Severity: alert.SeverityWarning, Title: "日志缓冲接近上限",
				Message: fmt.Sprintf("代理本地日志缓冲已使用 %d%%，请检查中央连接或磁盘空间", percentage),
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *Registry) ReconcileOffline(ctx context.Context) error {
	now := r.clock().UTC()
	serverIDs, err := r.repository.MarkOfflineBefore(ctx, now.Add(-15*time.Second))
	if err != nil {
		return err
	}
	for _, serverID := range serverIDs {
		if err := r.publisher.Publish(ctx, Event{
			Type:       "server.offline",
			ServerID:   serverID,
			OccurredAt: now,
		}); err != nil {
			return err
		}
	}
	return nil
}

type discardEventPublisher struct{}

func (discardEventPublisher) Publish(context.Context, Event) error {
	return nil
}
