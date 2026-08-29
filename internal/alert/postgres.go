package alert

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresRepository struct{ db *pgxpool.Pool }

func NewPostgresRepository(db *pgxpool.Pool) *PostgresRepository { return &PostgresRepository{db: db} }

func (r *PostgresRepository) MergeOrCreate(ctx context.Context, event Event, occurredAt, mergeSince time.Time) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	lockKey := fmt.Sprintf("%d:%s|%d:%s|%d:%s", len(event.ResourceType), event.ResourceType, len(event.ResourceID), event.ResourceID, len(event.Code), event.Code)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockKey); err != nil {
		return err
	}
	command, err := tx.Exec(ctx, `
		UPDATE alerts SET occurrence_count=occurrence_count+1, severity=$4, title=$5,
		       message=$6, last_occurred_at=$7, updated_at=$7
		WHERE id=(
			SELECT id FROM alerts
			WHERE kind=$1 AND source_type=$2 AND source_id=$3 AND status='open'
			  AND last_occurred_at >= $8
			ORDER BY last_occurred_at DESC LIMIT 1
		)
	`, event.Code, event.ResourceType, event.ResourceID, event.Severity, event.Title,
		event.Message, occurredAt, mergeSince)
	if err != nil {
		return fmt.Errorf("合并告警：%w", err)
	}
	if command.RowsAffected() == 0 {
		_, err = tx.Exec(ctx, `
			INSERT INTO alerts (
				kind, severity, source_type, source_id, title, message,
				first_occurred_at, last_occurred_at, created_at, updated_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$7,$7,$7)
		`, event.Code, event.Severity, event.ResourceType, event.ResourceID,
			event.Title, event.Message, occurredAt)
		if err != nil {
			return fmt.Errorf("创建告警：%w", err)
		}
	}
	return tx.Commit(ctx)
}

func (r *PostgresRepository) List(ctx context.Context) ([]Alert, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id::text, source_type, source_id, kind, severity, title, message, status,
		       occurrence_count, first_occurred_at, last_occurred_at,
		       COALESCE(acknowledged_by::text,''), acknowledged_at
		FROM alerts ORDER BY CASE status WHEN 'open' THEN 0 WHEN 'acknowledged' THEN 1 ELSE 2 END,
		       last_occurred_at DESC LIMIT 200
	`)
	if err != nil {
		return nil, fmt.Errorf("读取告警：%w", err)
	}
	defer rows.Close()
	items := []Alert{}
	for rows.Next() {
		var item Alert
		if err := rows.Scan(&item.ID, &item.ResourceType, &item.ResourceID, &item.Code,
			&item.Severity, &item.Title, &item.Message, &item.Status, &item.Occurrences,
			&item.FirstOccurredAt, &item.LastOccurredAt, &item.AcknowledgedBy,
			&item.AcknowledgedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *PostgresRepository) Acknowledge(ctx context.Context, id, userID string, at time.Time) error {
	command, err := r.db.Exec(ctx, `
		UPDATE alerts SET status='acknowledged', acknowledged_by=$2,
		       acknowledged_at=$3, updated_at=$3
		WHERE id=$1 AND status='open'
	`, id, userID, at)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 0 {
		return ErrAlertNotFound
	}
	return nil
}

var _ Repository = (*PostgresRepository)(nil)
