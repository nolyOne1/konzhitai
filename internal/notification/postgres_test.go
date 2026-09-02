package notification_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"yunling.local/platform/internal/notification"
	"yunling.local/platform/internal/secret"
	"yunling.local/platform/internal/testpostgres"
)

func TestPostgresConfigStoresOnlySecretReferencesAndAuditsUpdate(t *testing.T) {
	db := notificationDatabase(t)
	ctx := context.Background()
	actorID := insertNotificationUser(t, db)
	repository := notification.NewPostgresRepository(db)
	secretService := secret.NewService(secret.NewPostgresRepository(db), notificationKeyProvider())
	service := notification.NewConfigService(repository, secretService)

	view, err := service.Update(ctx, actorID, "127.0.0.1", notification.FeishuConfigInput{
		Enabled: true, Webhook: validWebhook, SigningSecret: "signing-secret-value",
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(validWebhook)) || bytes.Contains(encoded, []byte("signing-secret-value")) {
		t.Fatalf("配置视图泄露秘密：%s", encoded)
	}

	var webhookID, signingID, masked string
	if err := db.QueryRow(ctx, `
		SELECT webhook_secret_id::text, signing_secret_id::text, masked_destination
		FROM notification_configs WHERE channel='feishu'
	`).Scan(&webhookID, &signingID, &masked); err != nil {
		t.Fatal(err)
	}
	if webhookID == "" || signingID == "" || masked != "飞书机器人 …cdef" {
		t.Fatalf("数据库配置引用不完整：webhook=%q signing=%q masked=%q", webhookID, signingID, masked)
	}
	var action string
	if err := db.QueryRow(ctx, `
		SELECT action FROM audit_logs WHERE target_type='notification' AND target_id='feishu'
		ORDER BY created_at DESC LIMIT 1
	`).Scan(&action); err != nil {
		t.Fatal(err)
	}
	if action != "operations.feishu.update" {
		t.Fatalf("审计动作错误：%q", action)
	}

	if _, err := service.Update(ctx, actorID, "127.0.0.1", notification.FeishuConfigInput{
		Enabled:       true,
		Webhook:       "https://open.feishu.cn/open-apis/bot/v2/hook/11111111-2222-4333-8444-555555555555",
		SigningSecret: "rotated-signing-secret",
	}); err != nil {
		t.Fatal(err)
	}
	var internalSecretCount int
	if err := db.QueryRow(ctx, `
		SELECT count(*) FROM secrets
		WHERE scope='system' AND name LIKE 'notification/feishu/%'
	`).Scan(&internalSecretCount); err != nil {
		t.Fatal(err)
	}
	if internalSecretCount != 2 {
		t.Fatalf("轮换后只应保留两个被引用的系统秘密，实际 %d", internalSecretCount)
	}
}

func TestPostgresOutboxClaimsOnceAndExpiredLeaseCanBeTakenOver(t *testing.T) {
	db := notificationDatabase(t)
	ctx := context.Background()
	actorID := insertNotificationUser(t, db)
	repository := notification.NewPostgresRepository(db)
	secretService := secret.NewService(secret.NewPostgresRepository(db), notificationKeyProvider())
	configService := notification.NewConfigService(repository, secretService)
	if _, err := configService.Update(ctx, actorID, "127.0.0.1", notification.FeishuConfigInput{
		Enabled: true, Webhook: validWebhook, SigningSecret: "signing-secret-value",
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	if _, err := db.Exec(ctx, `
		INSERT INTO notification_outbox (
			event_type, payload, idempotency_key,
			next_attempt_at, created_at, updated_at
		)
		VALUES ('test', $1, 'claim-test', $2, $2, $2)
	`, `{"code":"test","severity":"info","title":"并发领取测试","sourceType":"system","sourceId":"yunling","occurrenceCount":1,"occurredAt":"2026-08-31T12:00:00Z"}`, now); err != nil {
		t.Fatal(err)
	}

	type result struct {
		claim notification.ClaimedDelivery
		ok    bool
		err   error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			claim, ok, err := repository.ClaimDue(ctx, now, 30*time.Second)
			results <- result{claim: claim, ok: ok, err: err}
		}()
	}
	claimed := 0
	var first notification.ClaimedDelivery
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.ok {
			claimed++
			first = result.claim
		}
	}
	if claimed != 1 || first.Attempts != 1 {
		t.Fatalf("同一发件箱项只能领取一次：claimed=%d delivery=%+v", claimed, first)
	}

	takenOver, ok, err := repository.ClaimDue(ctx, now.Add(31*time.Second), 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || takenOver.ID != first.ID || takenOver.Attempts != 2 {
		t.Fatalf("租约过期后应接管同一项：ok=%v delivery=%+v", ok, takenOver)
	}
}

func notificationDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()
	db := testpostgres.Start(t)
	root := testpostgres.RepositoryRoot(t)
	paths, err := filepath.Glob(filepath.Join(root, "migrations", "*.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		testpostgres.ApplyMigration(t, db, filepath.Base(path))
	}
	return db
}

func insertNotificationUser(t *testing.T, db *pgxpool.Pool) string {
	t.Helper()
	var id string
	if err := db.QueryRow(context.Background(), `
		INSERT INTO users (email, display_name, password_hash)
		VALUES ($1, '通知管理员', 'test-hash') RETURNING id
	`, fmt.Sprintf("notification-%d@example.com", time.Now().UnixNano())).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

type notificationStaticKeyProvider struct{ key secret.MasterKey }

func notificationKeyProvider() notificationStaticKeyProvider {
	return notificationStaticKeyProvider{key: secret.MasterKey{Version: 1, Material: bytes.Repeat([]byte{0x35}, 32)}}
}

func (p notificationStaticKeyProvider) Current(context.Context) (secret.MasterKey, error) {
	return p.key, nil
}

func (p notificationStaticKeyProvider) ByVersion(_ context.Context, version int) (secret.MasterKey, error) {
	key := p.key
	key.Version = version
	return key, nil
}
