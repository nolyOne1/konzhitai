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

var _ ConfigRepository = (*PostgresRepository)(nil)
