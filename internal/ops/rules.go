package ops

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"yunling.local/platform/internal/alert"
)

const (
	offlineThreshold = 2 * time.Minute
	queueThreshold   = 10 * time.Minute
	snapshotMaxAge   = 2 * time.Minute
)

type RuleKey struct {
	Code       string
	SourceType string
	SourceID   string
}

type RuleState struct {
	Active          bool
	DesiredActive   bool
	ConsecutiveBad  int
	ConsecutiveGood int
	LastValue       *float64
	LastEvaluatedAt time.Time
}

type ResourceSample struct {
	MemoryTotalBytes     int64
	MemoryAvailableBytes int64
	DiskTotalBytes       int64
	DiskAvailableBytes   int64
	CollectedAt          time.Time
}

type ServerObservation struct {
	ID         string
	Name       string
	Enabled    bool
	Draining   bool
	LastSeenAt *time.Time
	Samples    []ResourceSample
}

type RunObservation struct {
	ID       string
	TaskName string
	State    string
	QueuedAt time.Time
}

type RuleSnapshot struct {
	Servers []ServerObservation
	Runs    []RunObservation
}

type Evaluation struct {
	Key                 RuleKey
	Bad                 bool
	Good                bool
	Value               *float64
	EvaluatedAt         time.Time
	RequiredConsecutive int
	SampleBased         bool
	Event               alert.Event
}

type Transition struct {
	Evaluation
	DesiredActive bool
}

type RuleRepository interface {
	Snapshot(context.Context, time.Time) (RuleSnapshot, error)
	Apply(context.Context, []Evaluation) ([]Transition, error)
	MarkApplied(context.Context, RuleKey, bool) error
}

type AlertSink interface {
	Raise(context.Context, alert.Event) error
	Resolve(context.Context, string, string, string) error
}

type RuleEngine struct {
	repository RuleRepository
	alerts     AlertSink
	now        func() time.Time
}

func NewRuleEngine(repository RuleRepository, alerts AlertSink, now func() time.Time) *RuleEngine {
	if now == nil {
		now = time.Now
	}
	return &RuleEngine{repository: repository, alerts: alerts, now: now}
}

func (e *RuleEngine) Scan(ctx context.Context) error {
	if e == nil || e.repository == nil || e.alerts == nil {
		return errors.New("运维规则服务尚未配置")
	}
	now := e.now().UTC()
	snapshot, err := e.repository.Snapshot(ctx, now)
	if err != nil {
		return err
	}
	evaluations := evaluateSnapshot(snapshot, now)
	transitions, err := e.repository.Apply(ctx, evaluations)
	if err != nil {
		return err
	}
	var failures []error
	for _, transition := range deduplicateTransitions(transitions) {
		if err := e.applyTransition(ctx, transition); err != nil {
			failures = append(failures, err)
			continue
		}
		if err := e.repository.MarkApplied(ctx, transition.Key, transition.DesiredActive); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (e *RuleEngine) applyTransition(ctx context.Context, transition Transition) error {
	if transition.DesiredActive {
		return e.alerts.Raise(ctx, transition.Event)
	}
	return e.alerts.Resolve(ctx, transition.Key.SourceType, transition.Key.SourceID, transition.Key.Code)
}

func evaluateSnapshot(snapshot RuleSnapshot, now time.Time) []Evaluation {
	evaluations := make([]Evaluation, 0, len(snapshot.Servers)*5+len(snapshot.Runs)*2)
	for _, server := range snapshot.Servers {
		evaluations = append(evaluations, evaluateServerAvailability(server, now))
		if !server.Enabled {
			evaluations = append(evaluations,
				resourceEvaluation(server, "memory_low", "服务器可用内存不足", nil, false, true, now, 1),
				resourceEvaluation(server, "disk_low", "服务器可用磁盘不足", nil, false, true, now, 1),
			)
			continue
		}
		samples := append([]ResourceSample(nil), server.Samples...)
		sort.Slice(samples, func(i, j int) bool { return samples[i].CollectedAt.Before(samples[j].CollectedAt) })
		for _, sample := range samples {
			if sample.CollectedAt.After(now) || now.Sub(sample.CollectedAt) > snapshotMaxAge {
				continue
			}
			if sample.MemoryTotalBytes > 0 {
				value := float64(sample.MemoryAvailableBytes) / float64(sample.MemoryTotalBytes) * 100
				evaluations = append(evaluations, resourceEvaluation(
					server, "memory_low", "服务器可用内存不足", &value, value < 10, value > 15,
					sample.CollectedAt, 2,
				))
			}
			if sample.DiskTotalBytes > 0 {
				value := float64(sample.DiskAvailableBytes) / float64(sample.DiskTotalBytes) * 100
				evaluations = append(evaluations, resourceEvaluation(
					server, "disk_low", "服务器可用磁盘不足", &value, value < 15, value > 20,
					sample.CollectedAt, 2,
				))
			}
		}
	}
	for _, run := range snapshot.Runs {
		queueBad := run.State == "queued" && !run.QueuedAt.After(now.Add(-queueThreshold))
		evaluations = append(evaluations, Evaluation{
			Key: RuleKey{Code: "queue_timeout", SourceType: "run", SourceID: run.ID},
			Bad: queueBad, Good: !queueBad, EvaluatedAt: now, RequiredConsecutive: 1,
			Event: alert.Event{ResourceType: "run", ResourceID: run.ID, Code: "queue_timeout",
				Severity: alert.SeverityWarning, Title: "任务排队超过 10 分钟",
				Message: fmt.Sprintf("任务 %s 正在等待可用服务器", run.TaskName)},
		})
		if run.State == "failed" || run.State == "timed_out" {
			title := "任务执行失败"
			if run.State == "timed_out" {
				title = "任务执行超时"
			}
			evaluations = append(evaluations, Evaluation{
				Key: RuleKey{Code: "task_failed", SourceType: "run", SourceID: run.ID},
				Bad: true, EvaluatedAt: now, RequiredConsecutive: 1,
				Event: alert.Event{ResourceType: "run", ResourceID: run.ID, Code: "task_failed",
					Severity: alert.SeverityCritical, Title: title,
					Message: fmt.Sprintf("任务 %s 已结束，请在执行记录中查看状态", run.TaskName)},
			})
		}
	}
	return evaluations
}

func evaluateServerAvailability(server ServerObservation, now time.Time) Evaluation {
	bad := server.Enabled && (server.LastSeenAt == nil || !server.LastSeenAt.After(now.Add(-offlineThreshold)))
	return Evaluation{
		Key: RuleKey{Code: "agent_offline", SourceType: "server", SourceID: server.ID},
		Bad: bad, Good: !bad, EvaluatedAt: now, RequiredConsecutive: 1,
		Event: alert.Event{ResourceType: "server", ResourceID: server.ID, Code: "agent_offline",
			Severity: alert.SeverityCritical, Title: "服务器离线",
			Message: fmt.Sprintf("执行服务器 %s 已超过 2 分钟没有心跳", server.Name)},
	}
}

func resourceEvaluation(
	server ServerObservation,
	code, title string,
	value *float64,
	bad, good bool,
	evaluatedAt time.Time,
	required int,
) Evaluation {
	return Evaluation{
		Key: RuleKey{Code: code, SourceType: "server", SourceID: server.ID},
		Bad: bad, Good: good, Value: value, EvaluatedAt: evaluatedAt, RequiredConsecutive: required,
		SampleBased: value != nil,
		Event: alert.Event{ResourceType: "server", ResourceID: server.ID, Code: code,
			Severity: alert.SeverityWarning, Title: title,
			Message: fmt.Sprintf("执行服务器 %s 的可用资源低于告警阈值", server.Name)},
	}
}

func deduplicateTransitions(values []Transition) []Transition {
	latest := make(map[RuleKey]Transition, len(values))
	order := make([]RuleKey, 0, len(values))
	for _, value := range values {
		if _, exists := latest[value.Key]; !exists {
			order = append(order, value.Key)
		}
		latest[value.Key] = value
	}
	result := make([]Transition, 0, len(latest))
	for _, key := range order {
		result = append(result, latest[key])
	}
	return result
}
