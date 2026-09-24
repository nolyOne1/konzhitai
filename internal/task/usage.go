package task

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"yunling.local/platform/internal/agentprotocol"
)

// The caller holds the run row lock and has checked the reporting server and
// execution token. Samples are append-only; repeated heartbeats do not duplicate.
func saveRunUsage(ctx context.Context, tx pgx.Tx, runID string, usage *agentprotocol.ResourceUsage) error {
	if !usage.Valid() {
		return nil
	}
	var fresh bool
	if err := tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM run_events WHERE task_run_id=$1 AND payload->'usage' IS NOT NULL AND (payload->'usage'->>'sampledAt')::timestamptz >= $2)`, runID, usage.SampledAt).Scan(&fresh); err != nil {
		return fmt.Errorf("读取资源采样时间：%w", err)
	}
	if !fresh {
		return nil
	}
	return appendSystemRunEvent(ctx, tx, runID, "run.usage", Running, map[string]any{"usage": usage}, usage.SampledAt)
}

func (s *RunService) loadUsage(ctx context.Context, runID string) (*agentprotocol.ResourceUsage, error) {
	var raw []byte
	err := s.db.QueryRow(ctx, `SELECT payload->'usage' FROM run_events WHERE task_run_id=$1 AND payload->'usage' IS NOT NULL AND payload->'usage' <> 'null'::jsonb ORDER BY (payload->'usage'->>'sampledAt')::timestamptz DESC, sequence DESC LIMIT 1`, runID).Scan(&raw)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var usage agentprotocol.ResourceUsage
	if err := json.Unmarshal(raw, &usage); err != nil {
		return nil, err
	}
	return &usage, nil
}
