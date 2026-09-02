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

func TestPostgresResolveAcknowledgedAlertEnqueuesOneRecovery(t *testing.T) {
	db := testpostgres.Start(t)
	testpostgres.ApplyInitialMigration(t, db)
	for _, migration := range []string{
		"000002_agent_enrollment.up.sql", "000008_security_audit_alerts.up.sql",
		"000010_password_change_security.up.sql", "000011_notifications.up.sql",
	} {
		testpostgres.ApplyMigration(t, db, migration)
	}
	ctx := context.Background()
	var webhookID, signingID string
	for name, destination := range map[string]*string{
		"notification/feishu/webhook/alert-test": &webhookID,
		"notification/feishu/signing/alert-test": &signingID,
	} {
		if err := db.QueryRow(ctx, `
			INSERT INTO secrets (
				name, ciphertext, nonce, encrypted_data_key, data_key_nonce, key_version, scope
			) VALUES ($1, '\x01', '\x02', '\x03', '\x04', 1, 'system') RETURNING id
		`, name).Scan(destination); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO notification_configs (
			channel, enabled, webhook_secret_id, signing_secret_id, masked_destination
		) VALUES ('feishu', true, $1, $2, '飞书机器人 …test')
	`, webhookID, signingID); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	service := alert.NewService(alert.NewPostgresRepository(db), func() time.Time { return now })
	event := alert.Event{ResourceType: "server", ResourceID: "server-1", Code: "agent_offline", Severity: alert.SeverityCritical, Title: "服务器离线"}
	if err := service.Raise(ctx, event); err != nil {
		t.Fatal(err)
	}
	items, err := service.List(ctx)
	if err != nil || len(items) != 1 {
		t.Fatalf("创建告警失败：items=%+v err=%v", items, err)
	}
	var userID string
	if err := db.QueryRow(ctx, `
		INSERT INTO users (email, display_name, password_hash)
		VALUES ($1, '恢复处理人', 'test-hash') RETURNING id
	`, fmt.Sprintf("resolve-%d@example.com", time.Now().UnixNano())).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if err := service.Acknowledge(ctx, items[0].ID, userID); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if err := service.Resolve(ctx, "server", "server-1", "agent_offline"); err != nil {
		t.Fatal(err)
	}
	if err := service.Resolve(ctx, "server", "server-1", "agent_offline"); err != nil {
		t.Fatal(err)
	}
	items, err = service.List(ctx)
	if err != nil || items[0].Status != alert.StatusResolved || items[0].ResolvedAt == nil {
		t.Fatalf("恢复告警未持久化：items=%+v err=%v", items, err)
	}
	var recovered int
	if err := db.QueryRow(ctx, `
		SELECT count(*) FROM notification_outbox
		WHERE alert_id=$1 AND event_type='recovered'
	`, items[0].ID).Scan(&recovered); err != nil {
		t.Fatal(err)
	}
	if recovered != 1 {
		t.Fatalf("重复恢复只能生成一条恢复通知，实际 %d", recovered)
	}
}
