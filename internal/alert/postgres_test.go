package alert_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"yunling.local/platform/internal/alert"
	"yunling.local/platform/internal/testpostgres"
)

func TestPostgresRepositoryMergesAndAcknowledgesAlerts(t *testing.T) {
	db := testpostgres.Start(t)
	testpostgres.ApplyInitialMigration(t, db)
	testpostgres.ApplyMigration(t, db, "000002_agent_enrollment.up.sql")
	testpostgres.ApplyMigration(t, db, "000008_security_audit_alerts.up.sql")
	ctx := context.Background()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	service := alert.NewService(alert.NewPostgresRepository(db), func() time.Time { return now })
	event := alert.Event{ResourceType: "server", ResourceID: "server-1", Code: "agent_offline", Severity: alert.SeverityCritical, Title: "服务器离线", Message: "代理已离线"}

	if err := service.Raise(ctx, event); err != nil {
		t.Fatal(err)
	}
	now = now.Add(4 * time.Minute)
	if err := service.Raise(ctx, event); err != nil {
		t.Fatal(err)
	}
	items, err := service.List(ctx)
	if err != nil || len(items) != 1 || items[0].Occurrences != 2 {
		t.Fatalf("PostgreSQL 告警合并失败：items=%+v err=%v", items, err)
	}
	var userID string
	if err := db.QueryRow(ctx, `
		INSERT INTO users (email, display_name, password_hash)
		VALUES ($1, '告警处理人', 'test-hash') RETURNING id
	`, fmt.Sprintf("alert-%d@example.com", time.Now().UnixNano())).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if err := service.Acknowledge(ctx, items[0].ID, userID); err != nil {
		t.Fatal(err)
	}
	items, err = service.List(ctx)
	if err != nil || items[0].Status != alert.StatusAcknowledged || items[0].AcknowledgedBy != userID {
		t.Fatalf("确认告警未持久化：items=%+v err=%v", items, err)
	}
}
