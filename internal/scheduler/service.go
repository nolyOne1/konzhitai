package scheduler

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"yunling.local/platform/internal/alert"
	"yunling.local/platform/internal/task"
)

type Service struct {
	runs    RunStore
	servers ServerSource
	leases  LeaseStore
	queue   *Queue
	now     func() time.Time
	alerts  schedulerAlertSink
}

type schedulerAlertSink interface {
	Raise(context.Context, alert.Event) error
}

type ServiceOption func(*Service)

func WithAlertSink(sink schedulerAlertSink) ServiceOption {
	return func(service *Service) { service.alerts = sink }
}

func NewService(runs RunStore, servers ServerSource, leases LeaseStore, now func() time.Time, options ...ServiceOption) *Service {
	if now == nil {
		now = time.Now
	}
	service := &Service{runs: runs, servers: servers, leases: leases, queue: NewQueue(), now: now}
	for _, option := range options {
		option(service)
	}
	return service
}

func (s *Service) ScheduleOne(ctx context.Context, runID string) (Outcome, error) {
	run, err := s.runs.Get(ctx, runID)
	if err != nil {
		return OutcomeQueued, err
	}
	if run.State != task.Queued {
		if run.State == task.Expired {
			return OutcomeExpired, nil
		}
		return OutcomeQueued, nil
	}
	now := s.now()
	if run.MaxWaitSeconds > 0 && !now.Before(run.QueuedAt.Add(time.Duration(run.MaxWaitSeconds)*time.Second)) {
		expired, err := s.runs.Expire(ctx, run.ID, now)
		if err != nil {
			return OutcomeQueued, err
		}
		if expired {
			s.queue.Remove(run.ID)
			if s.alerts != nil {
				if err := s.alerts.Raise(ctx, alert.Event{
					ResourceType: "task_run", ResourceID: run.ID, Code: "task_queue_timeout",
					Severity: alert.SeverityWarning, Title: "任务排队超时",
					Message: "在最大等待时间内没有可分配的服务器，任务已停止排队",
				}); err != nil {
					return OutcomeExpired, fmt.Errorf("生成任务排队告警：%w", err)
				}
			}
			return OutcomeExpired, nil
		}
	}
	active, err := s.runs.CountActive(ctx, run.DefinitionID)
	if err != nil {
		return OutcomeQueued, err
	}
	if run.MaxConcurrency > 0 && active >= run.MaxConcurrency {
		s.queue.Push(run)
		return OutcomeQueued, nil
	}
	snapshots, err := s.servers.Snapshots(ctx, run)
	if err != nil {
		return OutcomeQueued, err
	}
	for _, candidate := range RankCandidates(run, Filter(run, snapshots)) {
		lease, reserved, err := s.leases.TryReserve(ctx, LeaseRequest{
			RunID: run.ID, ServerID: candidate.ServerID,
			Available: candidate.Available, Required: run.Resources,
			Now: now, TTL: leaseTTL(run),
		})
		if err != nil {
			return OutcomeQueued, err
		}
		if !reserved {
			continue
		}
		assignment := Assignment{
			RunID: run.ID, ServerID: candidate.ServerID,
			ScriptVersionID: run.ScriptVersionID, ExecutionToken: uuid.NewString(),
			Lease: lease, AssignedAt: now,
		}
		assigned, err := s.runs.Assign(ctx, assignment)
		if err != nil {
			_ = s.leases.Release(ctx, lease)
			return OutcomeQueued, err
		}
		if !assigned {
			_ = s.leases.Release(ctx, lease)
			return OutcomeQueued, nil
		}
		s.queue.Remove(run.ID)
		return OutcomeAssigned, nil
	}
	s.queue.Push(run)
	return OutcomeQueued, nil
}

func (s *Service) HandleEvent(ctx context.Context, event Event) error {
	switch event.Type {
	case RunQueued:
		if event.RunID == "" {
			return ErrInvalidEvent
		}
		_, err := s.ScheduleOne(ctx, event.RunID)
		return err
	case ResourceReleased, ServerOnline, ServerChanged, ScriptReady:
		return s.Scan(ctx)
	default:
		return ErrInvalidEvent
	}
}

func (s *Service) Scan(ctx context.Context) error {
	runs, err := s.runs.ListQueued(ctx)
	if err != nil {
		return err
	}
	for _, run := range runs {
		s.queue.Push(run)
	}
	count := s.queue.Len()
	for index := 0; index < count; index++ {
		run, ok := s.queue.Pop()
		if !ok {
			break
		}
		if _, err := s.ScheduleOne(ctx, run.ID); err != nil {
			return err
		}
	}
	return nil
}

func leaseTTL(run task.Run) time.Duration {
	timeout := time.Duration(run.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = time.Hour
	}
	return timeout + 2*time.Minute
}
