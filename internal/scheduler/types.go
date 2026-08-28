package scheduler

import (
	"context"
	"errors"
	"time"

	"yunling.local/platform/internal/server"
	"yunling.local/platform/internal/task"
)

var (
	ErrRunNotFound         = errors.New("运行实例不存在")
	ErrInvalidEvent        = errors.New("调度事件无效")
	ErrInvalidLeaseRequest = errors.New("资源租约申请无效")
)

type Outcome string

const (
	OutcomeAssigned Outcome = "assigned"
	OutcomeQueued   Outcome = "queued"
	OutcomeExpired  Outcome = "expired"
)

type EventType string

const (
	RunQueued        EventType = "run.queued"
	ResourceReleased EventType = "resource.released"
	ServerOnline     EventType = "server.online"
	ServerChanged    EventType = "server.changed"
	ScriptReady      EventType = "script.ready"
)

type Event struct {
	Type     EventType
	RunID    string
	ServerID string
}

type Candidate struct {
	ServerID         string
	Total            task.Resources
	Available        task.Resources
	RunningTasks     int
	MaxConcurrency   int
	ScriptCached     bool
	FairnessScore    int64
	SchedulingWeight int
}

type LeaseRequest struct {
	RunID     string
	ServerID  string
	Available task.Resources
	Required  task.Resources
	Now       time.Time
	TTL       time.Duration
}

type Lease struct {
	ID        string
	RunID     string
	ServerID  string
	Resources task.Resources
	ExpiresAt time.Time
}

type Assignment struct {
	RunID           string
	ServerID        string
	ScriptVersionID string
	Lease           Lease
	AssignedAt      time.Time
}

type RunStore interface {
	Get(context.Context, string) (task.Run, error)
	ListQueued(context.Context) ([]task.Run, error)
	CountActive(context.Context, string) (int, error)
	Assign(context.Context, Assignment) (bool, error)
	Expire(context.Context, string, time.Time) (bool, error)
}

type ServerSource interface {
	Snapshots(context.Context, task.Run) ([]server.Snapshot, error)
}

type LeaseStore interface {
	TryReserve(context.Context, LeaseRequest) (Lease, bool, error)
	Release(context.Context, Lease) error
}
