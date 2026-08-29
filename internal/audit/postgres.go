package audit

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresRepository struct{ db *pgxpool.Pool }

func NewPostgresRepository(db *pgxpool.Pool) *PostgresRepository {
	return &PostgresRepository{db: db}
}

func (r *PostgresRepository) Append(ctx context.Context, event Event) error {
	details, err := json.Marshal(event.Details)
	if err != nil {
		return ErrInvalidEvent
	}
	var actorID, ipAddress any
	if event.ActorID != "" {
		actorID = event.ActorID
	}
	if event.IPAddress != "" {
		ipAddress = event.IPAddress
	}
	_, err = r.db.Exec(ctx, `
		INSERT INTO audit_logs (id, actor_id, action, target_type, target_id, details, ip_address, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
	`, event.ID, actorID, event.Action, event.TargetType, event.TargetID, details, ipAddress, event.CreatedAt)
	if err != nil {
		return fmt.Errorf("写入审计日志：%w", err)
	}
	return nil
}

func (r *PostgresRepository) List(ctx context.Context, filter Filter) ([]Event, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id::text, COALESCE(actor_id::text,''), action, target_type, target_id,
		       details, COALESCE(ip_address::text,''), created_at
		FROM audit_logs
		WHERE ($1='' OR actor_id::text=$1)
		  AND ($2='' OR action=$2)
		  AND ($3='' OR target_type=$3)
		ORDER BY created_at DESC, id DESC
		LIMIT $4
	`, filter.ActorID, filter.Action, filter.TargetType, filter.Limit)
	if err != nil {
		return nil, fmt.Errorf("读取审计日志：%w", err)
	}
	defer rows.Close()
	events := []Event{}
	for rows.Next() {
		var event Event
		var details []byte
		if err := rows.Scan(&event.ID, &event.ActorID, &event.Action, &event.TargetType,
			&event.TargetID, &details, &event.IPAddress, &event.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(details, &event.Details); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

var _ Repository = (*PostgresRepository)(nil)
