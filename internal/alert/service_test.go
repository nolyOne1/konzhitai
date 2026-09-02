package alert_test

import (
	"context"
	"testing"
	"time"

	"yunling.local/platform/internal/alert"
)

func TestRaiseMergesDuplicateAlertWithinFiveMinuteWindow(t *testing.T) {
	repository := &memoryAlertRepository{}
	now := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
	service := alert.NewService(repository, func() time.Time { return now })
	event := alert.Event{
		ResourceType: "server", ResourceID: "server-1", Code: "agent_offline",
		Severity: alert.SeverityCritical, Title: "服务器离线", Message: "代理已离线",
	}

	if err := service.Raise(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	now = now.Add(4 * time.Minute)
	if err := service.Raise(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if len(repository.alerts) != 1 || repository.alerts[0].Occurrences != 2 {
		t.Fatalf("五分钟内同一资源与错误码应合并：%+v", repository.alerts)
	}
	if repository.alerts[0].LastOccurredAt != now {
		t.Fatalf("合并后应更新时间：%+v", repository.alerts[0])
	}
}

func TestRaiseCreatesNewAlertAfterMergeWindow(t *testing.T) {
	repository := &memoryAlertRepository{}
	now := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
	service := alert.NewService(repository, func() time.Time { return now })
	event := alert.Event{ResourceType: "script", ResourceID: "script-1", Code: "sync_failed", Severity: alert.SeverityWarning, Title: "脚本同步失败"}

	if err := service.Raise(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	now = now.Add(6 * time.Minute)
	if err := service.Raise(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if len(repository.alerts) != 2 {
		t.Fatalf("超过五分钟窗口应新建告警，实际为 %d", len(repository.alerts))
	}
}

func TestResolveIsIdempotentAndIncludesResolvedTime(t *testing.T) {
	repository := &memoryAlertRepository{}
	now := time.Date(2026, 8, 31, 10, 0, 0, 0, time.UTC)
	service := alert.NewService(repository, func() time.Time { return now })
	event := alert.Event{ResourceType: "server", ResourceID: "server-1", Code: "agent_offline", Severity: alert.SeverityCritical, Title: "服务器离线"}
	if err := service.Raise(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	repository.alerts[0].Status = alert.StatusAcknowledged
	now = now.Add(time.Minute)
	if err := service.Resolve(context.Background(), "server", "server-1", "agent_offline"); err != nil {
		t.Fatal(err)
	}
	if err := service.Resolve(context.Background(), "server", "server-1", "agent_offline"); err != nil {
		t.Fatal(err)
	}
	if repository.alerts[0].Status != alert.StatusResolved || repository.alerts[0].ResolvedAt == nil || !repository.alerts[0].ResolvedAt.Equal(now) {
		t.Fatalf("告警未正确恢复：%+v", repository.alerts[0])
	}
}

type memoryAlertRepository struct{ alerts []alert.Alert }

func (r *memoryAlertRepository) MergeOrCreate(_ context.Context, event alert.Event, occurredAt, mergeSince time.Time) error {
	for index := range r.alerts {
		item := &r.alerts[index]
		if item.Status == alert.StatusOpen && item.ResourceType == event.ResourceType && item.ResourceID == event.ResourceID && item.Code == event.Code && !item.LastOccurredAt.Before(mergeSince) {
			item.Occurrences++
			item.LastOccurredAt = occurredAt
			item.Message = event.Message
			return nil
		}
	}
	r.alerts = append(r.alerts, alert.Alert{
		ID: "alert-new", Event: event, Status: alert.StatusOpen, Occurrences: 1,
		FirstOccurredAt: occurredAt, LastOccurredAt: occurredAt,
	})
	return nil
}

func (r *memoryAlertRepository) List(context.Context) ([]alert.Alert, error) {
	return append([]alert.Alert(nil), r.alerts...), nil
}

func (r *memoryAlertRepository) Acknowledge(context.Context, string, string, time.Time) error {
	return nil
}

func (r *memoryAlertRepository) Resolve(_ context.Context, resourceType, resourceID, code string, at time.Time) error {
	for index := range r.alerts {
		item := &r.alerts[index]
		if item.ResourceType == resourceType && item.ResourceID == resourceID && item.Code == code &&
			(item.Status == alert.StatusOpen || item.Status == alert.StatusAcknowledged) {
			item.Status = alert.StatusResolved
			item.ResolvedAt = &at
			return nil
		}
	}
	return nil
}
