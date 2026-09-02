package postgres

import (
	"context"
	"strings"
	"testing"
)

func TestInitialMigrationCreatesCoreTables(t *testing.T) {
	db := startPostgres(t)
	applyMigrations(t, db)

	for _, table := range []string{
		"servers",
		"script_versions",
		"task_runs",
		"resource_leases",
		"audit_logs",
	} {
		if !tableExists(t, db, table) {
			t.Errorf("初始迁移后应存在数据表 %q", table)
		}
	}
}

func TestPasswordChangeMigrationCreatesRateLimitTable(t *testing.T) {
	db := startPostgres(t)
	applyMigrations(t, db)

	if !tableExists(t, db, "auth_rate_limits") {
		t.Fatal("改密安全迁移后应存在 auth_rate_limits")
	}
}

func TestNotificationMigrationCreatesAtomicAlertOutbox(t *testing.T) {
	db := startPostgres(t)
	applyMigrations(t, db)
	ctx := context.Background()

	for _, table := range []string{"notification_configs", "notification_outbox", "alert_rule_states"} {
		if !tableExists(t, db, table) {
			t.Fatalf("通知迁移后应存在数据表 %q", table)
		}
	}

	var defaultScope string
	if err := db.QueryRow(ctx, `
		INSERT INTO secrets (name, ciphertext, nonce, encrypted_data_key, data_key_nonce, key_version)
		VALUES ('通知迁移默认作用域', '\x01', '\x02', '\x03', '\x04', 1)
		RETURNING scope
	`).Scan(&defaultScope); err != nil {
		t.Fatal(err)
	}
	if defaultScope != "user" {
		t.Fatalf("秘密默认作用域应为 user，实际为 %q", defaultScope)
	}

	var webhookID, signingID string
	for name, destination := range map[string]*string{
		"notification/feishu/webhook/migration": &webhookID,
		"notification/feishu/signing/migration": &signingID,
	} {
		if err := db.QueryRow(ctx, `
			INSERT INTO secrets (name, ciphertext, nonce, encrypted_data_key, data_key_nonce, key_version, scope)
			VALUES ($1, '\x01', '\x02', '\x03', '\x04', 1, 'system')
			RETURNING id
		`, name).Scan(destination); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO notification_configs (
			channel, enabled, webhook_secret_id, signing_secret_id, masked_destination
		) VALUES ('feishu', true, $1, $2, '飞书机器人 …cdef')
	`, webhookID, signingID); err != nil {
		t.Fatal(err)
	}

	var alertID string
	if err := db.QueryRow(ctx, `
		INSERT INTO alerts (
			kind, severity, source_type, source_id, title, message,
			first_occurred_at, last_occurred_at
		) VALUES (
			'agent_offline', 'warning', 'server', 'server-1', '执行服务器离线',
			'不得复制到通知的底层诊断', now(), now()
		) RETURNING id
	`).Scan(&alertID); err != nil {
		t.Fatal(err)
	}

	var eventType, payloadText string
	if err := db.QueryRow(ctx, `
		SELECT event_type, payload::text FROM notification_outbox
		WHERE alert_id=$1 AND idempotency_key=$2
	`, alertID, "alert:"+alertID+":opened").Scan(&eventType, &payloadText); err != nil {
		t.Fatal(err)
	}
	if eventType != "opened" || strings.Contains(payloadText, "底层诊断") {
		t.Fatalf("开启通知载荷不安全：event=%q payload=%s", eventType, payloadText)
	}

	if _, err := db.Exec(ctx, `
		UPDATE alerts SET status='resolved', resolved_at=now(), updated_at=now() WHERE id=$1
	`, alertID); err != nil {
		t.Fatal(err)
	}
	var recoveredCount int
	if err := db.QueryRow(ctx, `
		SELECT count(*) FROM notification_outbox
		WHERE alert_id=$1 AND event_type='recovered'
	`, alertID).Scan(&recoveredCount); err != nil {
		t.Fatal(err)
	}
	if recoveredCount != 1 {
		t.Fatalf("恢复告警应原子生成一项通知，实际 %d", recoveredCount)
	}
}

func TestBackupRecoveryMigrationCreatesOperationalTables(t *testing.T) {
	db := startPostgres(t)
	applyMigrations(t, db)

	for _, table := range []string{"backup_runs", "restore_verifications"} {
		if !tableExists(t, db, table) {
			t.Fatalf("备份恢复迁移后应存在数据表 %q", table)
		}
	}

	for _, index := range []string{
		"backup_runs_due_idx",
		"backup_runs_active_lease_idx",
		"restore_verifications_due_idx",
		"restore_verifications_active_lease_idx",
	} {
		var exists bool
		if err := db.QueryRow(context.Background(), `
			SELECT to_regclass('public.' || $1) IS NOT NULL
		`, index).Scan(&exists); err != nil {
			t.Fatalf("检查索引 %s：%v", index, err)
		}
		if !exists {
			t.Fatalf("备份恢复迁移后应存在索引 %q", index)
		}
	}
}
