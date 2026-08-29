package audit_test

import (
	"context"
	"testing"
	"time"

	"yunling.local/platform/internal/audit"
)

func TestRecordAppendsImmutableEventWithServerTimestamp(t *testing.T) {
	repository := &memoryAuditRepository{}
	now := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
	service := audit.NewService(repository, func() time.Time { return now })
	event := audit.Event{
		ActorID: "user-1", Action: "secret.create", TargetType: "secret",
		TargetID: "secret-1", Details: map[string]any{"name": "生产令牌"},
		IPAddress: "203.0.113.10",
	}

	if err := service.Record(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if len(repository.events) != 1 {
		t.Fatalf("应只追加一条审计日志，实际为 %d", len(repository.events))
	}
	stored := repository.events[0]
	if stored.ID == "" || stored.CreatedAt != now || stored.Action != event.Action || stored.Details["name"] != "生产令牌" {
		t.Fatalf("审计日志字段不完整：%+v", stored)
	}
}

func TestRecordRejectsIncompleteAuditEvent(t *testing.T) {
	service := audit.NewService(&memoryAuditRepository{}, time.Now)
	if err := service.Record(context.Background(), audit.Event{Action: "secret.create"}); err != audit.ErrInvalidEvent {
		t.Fatalf("缺少目标信息应被拒绝，实际错误：%v", err)
	}
}

type memoryAuditRepository struct{ events []audit.Event }

func (r *memoryAuditRepository) Append(_ context.Context, event audit.Event) error {
	event.Details = cloneDetails(event.Details)
	r.events = append(r.events, event)
	return nil
}

func (r *memoryAuditRepository) List(context.Context, audit.Filter) ([]audit.Event, error) {
	return append([]audit.Event(nil), r.events...), nil
}

func cloneDetails(input map[string]any) map[string]any {
	result := make(map[string]any, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}
