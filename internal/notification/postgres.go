package notification

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresRepository struct{ db *pgxpool.Pool }

func NewPostgresRepository(db *pgxpool.Pool) *PostgresRepository {
	return &PostgresRepository{db: db}
}

func (r *PostgresRepository) Get(ctx context.Context) (ConfigRecord, bool, error) {
	if r == nil || r.db == nil {
		return ConfigRecord{}, false, ErrUnavailable
	}
	var record ConfigRecord
	err := r.db.QueryRow(ctx, `
		SELECT enabled, webhook_secret_id::text, signing_secret_id::text,
		       masked_destination, created_at, updated_at
		FROM notification_configs WHERE channel='feishu'
	`).Scan(
		&record.Enabled, &record.WebhookSecretID, &record.SigningSecretID,
		&record.MaskedDestination, &record.CreatedAt, &record.UpdatedAt,
	)
	if err == pgx.ErrNoRows {
		return ConfigRecord{}, false, nil
	}
	if err != nil {
		return ConfigRecord{}, false, fmt.Errorf("读取飞书通知配置：%w", err)
	}
	return record, true, nil
}

func (r *PostgresRepository) Save(
	ctx context.Context,
	record ConfigRecord,
	actorID, ipAddress, action string,
) (ConfigRecord, error) {
	if r == nil || r.db == nil || strings.TrimSpace(actorID) == "" {
		return ConfigRecord{}, ErrInvalidConfig
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return ConfigRecord{}, fmt.Errorf("开始飞书配置事务：%w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	err = tx.QueryRow(ctx, `
		INSERT INTO notification_configs (
			channel, enabled, webhook_secret_id, signing_secret_id,
			masked_destination, created_by, updated_by
		) VALUES ('feishu', $1, $2, $3, $4, $5, $5)
		ON CONFLICT (channel) DO UPDATE SET
			enabled=EXCLUDED.enabled,
			webhook_secret_id=EXCLUDED.webhook_secret_id,
			signing_secret_id=EXCLUDED.signing_secret_id,
			masked_destination=EXCLUDED.masked_destination,
			updated_by=EXCLUDED.updated_by,
			updated_at=now()
		RETURNING enabled, webhook_secret_id::text, signing_secret_id::text,
		          masked_destination, created_at, updated_at
	`, record.Enabled, record.WebhookSecretID, record.SigningSecretID,
		record.MaskedDestination, actorID,
	).Scan(
		&record.Enabled, &record.WebhookSecretID, &record.SigningSecretID,
		&record.MaskedDestination, &record.CreatedAt, &record.UpdatedAt,
	)
	if err != nil {
		return ConfigRecord{}, fmt.Errorf("保存飞书通知配置：%w", err)
	}

	details, _ := json.Marshal(map[string]any{"enabled": record.Enabled})
	var storedIP any
	if parsed := net.ParseIP(strings.TrimSpace(ipAddress)); parsed != nil {
		storedIP = parsed.String()
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_logs (
			id, actor_id, action, target_type, target_id, details, ip_address, created_at
		) VALUES ($1, $2, $3, 'notification', 'feishu', $4, $5, $6)
	`, uuid.NewString(), actorID, action, details, storedIP, time.Now().UTC()); err != nil {
		return ConfigRecord{}, fmt.Errorf("记录飞书配置审计：%w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ConfigRecord{}, fmt.Errorf("提交飞书配置事务：%w", err)
	}
	return record, nil
}

func (r *PostgresRepository) DeleteUnreferencedSystemSecrets(ctx context.Context) error {
	if r == nil || r.db == nil {
		return ErrUnavailable
	}
	_, err := r.db.Exec(ctx, `
		DELETE FROM secrets AS candidate
		WHERE candidate.scope='system'
		  AND candidate.name LIKE 'notification/feishu/%'
		  AND NOT EXISTS (
			SELECT 1 FROM notification_configs AS config
			WHERE config.webhook_secret_id=candidate.id
			   OR config.signing_secret_id=candidate.id
		  )
	`)
	if err != nil {
		return fmt.Errorf("清理未引用飞书系统秘密：%w", err)
	}
	return nil
}

func (r *PostgresRepository) EnqueueTest(
	ctx context.Context,
	actorID string,
	payload FrozenMessage,
	idempotencyKey string,
	at time.Time,
) (Delivery, error) {
	if r == nil || r.db == nil || strings.TrimSpace(actorID) == "" || strings.TrimSpace(idempotencyKey) == "" {
		return Delivery{}, ErrUnavailable
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return Delivery{}, fmt.Errorf("开始测试通知事务：%w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var enabled bool
	if err := tx.QueryRow(ctx, `
		SELECT enabled FROM notification_configs WHERE channel='feishu' FOR SHARE
	`).Scan(&enabled); err == pgx.ErrNoRows || !enabled {
		return Delivery{}, ErrNotConfigured
	} else if err != nil {
		return Delivery{}, fmt.Errorf("检查飞书通知配置：%w", err)
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return Delivery{}, ErrInvalidConfig
	}
	delivery := Delivery{ID: uuid.NewString()}
	if err := tx.QueryRow(ctx, `
		INSERT INTO notification_outbox (
			id, event_type, payload, idempotency_key, status,
			next_attempt_at, created_at, updated_at
		) VALUES ($1, 'test', $2, $3, 'pending', $4, $4, $4)
		RETURNING id::text, event_type, status, attempts, next_attempt_at,
		          lease_until, last_error, response_id, created_at, sent_at, updated_at
	`, delivery.ID, payloadJSON, idempotencyKey, at).Scan(
		&delivery.ID, &delivery.EventType, &delivery.Status, &delivery.Attempts,
		&delivery.NextAttemptAt, &delivery.LeaseUntil, &delivery.LastError,
		&delivery.ResponseID, &delivery.CreatedAt, &delivery.SentAt, &delivery.UpdatedAt,
	); err != nil {
		return Delivery{}, fmt.Errorf("创建飞书测试通知：%w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_logs (
			id, actor_id, action, target_type, target_id, details, created_at
		) VALUES ($1, $2, 'operations.feishu.test', 'notification_delivery', $3, '{}'::jsonb, $4)
	`, uuid.NewString(), actorID, delivery.ID, at); err != nil {
		return Delivery{}, fmt.Errorf("记录飞书测试审计：%w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Delivery{}, fmt.Errorf("提交飞书测试通知：%w", err)
	}
	return delivery, nil
}

func (r *PostgresRepository) ClaimDue(ctx context.Context, now time.Time, lease time.Duration) (ClaimedDelivery, bool, error) {
	if r == nil || r.db == nil {
		return ClaimedDelivery{}, false, ErrUnavailable
	}
	var claim ClaimedDelivery
	var payloadJSON []byte
	err := r.db.QueryRow(ctx, `
		WITH candidate AS (
			SELECT outbox.id
			FROM notification_outbox AS outbox
			WHERE EXISTS (
				SELECT 1 FROM notification_configs
				WHERE channel='feishu' AND enabled=true
			)
			  AND (
				(outbox.status IN ('pending', 'retrying') AND outbox.next_attempt_at <= $1)
				OR (outbox.status='sending' AND outbox.lease_until <= $1)
			  )
			ORDER BY outbox.next_attempt_at, outbox.created_at, outbox.id
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE notification_outbox AS outbox
		SET status='sending', attempts=outbox.attempts+1,
		    lease_until=$1 + $2::interval, updated_at=$1
		FROM candidate, notification_configs AS config
		WHERE outbox.id=candidate.id AND config.channel='feishu' AND config.enabled=true
		RETURNING outbox.id::text, outbox.event_type, outbox.status, outbox.attempts,
		          outbox.next_attempt_at, outbox.lease_until, outbox.last_error,
		          outbox.response_id, outbox.created_at, outbox.sent_at, outbox.updated_at,
		          outbox.payload, config.webhook_secret_id::text, config.signing_secret_id::text
	`, now, lease.String()).Scan(
		&claim.ID, &claim.EventType, &claim.Status, &claim.Attempts,
		&claim.NextAttemptAt, &claim.LeaseUntil, &claim.LastError, &claim.ResponseID,
		&claim.CreatedAt, &claim.SentAt, &claim.UpdatedAt, &payloadJSON,
		&claim.WebhookSecretID, &claim.SigningSecretID,
	)
	if err == pgx.ErrNoRows {
		return ClaimedDelivery{}, false, nil
	}
	if err != nil {
		return ClaimedDelivery{}, false, fmt.Errorf("领取通知发件箱：%w", err)
	}
	if err := json.Unmarshal(payloadJSON, &claim.Payload); err != nil {
		return ClaimedDelivery{}, false, fmt.Errorf("解析通知发件箱载荷：%w", err)
	}
	return claim, true, nil
}

func (r *PostgresRepository) MarkSent(ctx context.Context, id, responseID string, at time.Time) error {
	command, err := r.db.Exec(ctx, `
		UPDATE notification_outbox
		SET status='sent', response_id=$2, sent_at=$3, lease_until=NULL,
		    last_error='', updated_at=$3
		WHERE id=$1 AND status='sending'
	`, id, responseID, at)
	if err != nil {
		return fmt.Errorf("完成通知发送：%w", err)
	}
	if command.RowsAffected() != 1 {
		return ErrDeliveryNotFound
	}
	return nil
}

func (r *PostgresRepository) MarkFailed(
	ctx context.Context,
	id, lastError string,
	next time.Time,
	terminal bool,
	at time.Time,
) error {
	status := DeliveryRetrying
	if terminal {
		status = DeliveryFailed
	}
	if len(lastError) > 256 {
		lastError = lastError[:256]
	}
	command, err := r.db.Exec(ctx, `
		UPDATE notification_outbox
		SET status=$2, next_attempt_at=$3, lease_until=NULL,
		    last_error=$4, updated_at=$5
		WHERE id=$1 AND status='sending'
	`, id, status, next, lastError, at)
	if err != nil {
		return fmt.Errorf("记录通知发送失败：%w", err)
	}
	if command.RowsAffected() != 1 {
		return ErrDeliveryNotFound
	}
	return nil
}

func (r *PostgresRepository) GetDelivery(ctx context.Context, id string) (Delivery, error) {
	if r == nil || r.db == nil || strings.TrimSpace(id) == "" {
		return Delivery{}, ErrDeliveryNotFound
	}
	var delivery Delivery
	err := r.db.QueryRow(ctx, `
		SELECT id::text, event_type, status, attempts, next_attempt_at,
		       lease_until, last_error, response_id, created_at, sent_at, updated_at
		FROM notification_outbox WHERE id=$1
	`, id).Scan(
		&delivery.ID, &delivery.EventType, &delivery.Status, &delivery.Attempts,
		&delivery.NextAttemptAt, &delivery.LeaseUntil, &delivery.LastError,
		&delivery.ResponseID, &delivery.CreatedAt, &delivery.SentAt, &delivery.UpdatedAt,
	)
	if err == pgx.ErrNoRows {
		return Delivery{}, ErrDeliveryNotFound
	}
	if err != nil {
		return Delivery{}, fmt.Errorf("读取通知发送记录：%w", err)
	}
	return delivery, nil
}

var _ ConfigRepository = (*PostgresRepository)(nil)
var _ OutboxRepository = (*PostgresRepository)(nil)
