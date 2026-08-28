package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"yunling.local/platform/internal/agentprotocol"
)

var ErrRunNotRetryable = errors.New("任务不满足安全重试条件")

type ReconcileStore interface {
	MarkServerRunsUnknown(ctx context.Context, serverID string, at time.Time) error
	ReconcileRunning(ctx context.Context, report agentprotocol.RunningReport, at time.Time) error
	RetryRun(ctx context.Context, runID RunID, at time.Time) (RunID, error)
}

type PostgresReconcileStore struct{ db *pgxpool.Pool }

func NewPostgresReconcileStore(db *pgxpool.Pool) *PostgresReconcileStore {
	return &PostgresReconcileStore{db: db}
}

func (s *PostgresReconcileStore) MarkServerRunsUnknown(ctx context.Context, serverID string, at time.Time) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("开始服务器失联对账：%w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `
		UPDATE task_runs SET state='unknown', process_confirmed_gone=false, updated_at=$2
		WHERE assigned_server_id=$1 AND state IN ('assigned','syncing','running')
		RETURNING id
	`, serverID, at)
	if err != nil {
		return fmt.Errorf("标记待确认任务：%w", err)
	}
	var runIDs []string
	for rows.Next() {
		var runID string
		if err := rows.Scan(&runID); err != nil {
			rows.Close()
			return err
		}
		runIDs = append(runIDs, runID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, runID := range runIDs {
		if err := appendSystemRunEvent(ctx, tx, runID, "run.unknown", Unknown, map[string]any{"message": "执行服务器失联，等待代理重连确认"}, at); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *PostgresReconcileStore) ReconcileRunning(ctx context.Context, report agentprotocol.RunningReport, at time.Time) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("开始代理重连对账：%w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	active := make(map[string]string, len(report.Processes))
	for _, process := range report.Processes {
		active[process.RunID] = process.ExecutionToken
	}
	rows, err := tx.Query(ctx, `
		SELECT id::text, COALESCE(execution_token,''), state
		FROM task_runs
		WHERE assigned_server_id=$1 AND state IN ('assigned','syncing','running','unknown')
		FOR UPDATE
	`, report.ServerID)
	if err != nil {
		return fmt.Errorf("读取服务器运行任务：%w", err)
	}
	type candidate struct {
		id, token string
		state     RunState
	}
	var candidates []candidate
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.id, &item.token, &item.state); err != nil {
			rows.Close()
			return err
		}
		candidates = append(candidates, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, item := range candidates {
		reportedToken, running := active[item.id]
		if running && reportedToken == item.token {
			if _, err := tx.Exec(ctx, `UPDATE task_runs SET state='running', process_confirmed_gone=false, updated_at=$2 WHERE id=$1`, item.id, at); err != nil {
				return fmt.Errorf("恢复运行任务状态：%w", err)
			}
			if item.state != Running {
				if err := appendSystemRunEvent(ctx, tx, item.id, "run.reconciled", Running, map[string]any{"message": "代理重连后确认任务仍在运行"}, at); err != nil {
					return err
				}
			}
			continue
		}
		if !report.Authoritative {
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE task_runs SET state='unknown', process_confirmed_gone=true, updated_at=$2 WHERE id=$1`, item.id, at); err != nil {
			return fmt.Errorf("确认原任务进程已结束：%w", err)
		}
		if !running || reportedToken != item.token {
			if err := appendSystemRunEvent(ctx, tx, item.id, "run.process_absent", Unknown, map[string]any{"message": "代理已确认原执行进程不存在，可按策略重试"}, at); err != nil {
				return err
			}
		}
	}
	return tx.Commit(ctx)
}

func (s *PostgresReconcileStore) RetryRun(ctx context.Context, runID RunID, at time.Time) (RunID, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("开始任务重试事务：%w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var state RunState
	var idempotent, processGone bool
	var attempt, maxRetries int
	err = tx.QueryRow(ctx, `
		SELECT state, idempotent, process_confirmed_gone, attempt, max_retries
		FROM task_runs WHERE id=$1 FOR UPDATE
	`, runID).Scan(&state, &idempotent, &processGone, &attempt, &maxRetries)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrRunNotFound
	}
	if err != nil {
		return "", fmt.Errorf("读取任务重试条件：%w", err)
	}
	if !idempotent || !processGone || attempt > maxRetries ||
		(state != Failed && state != TimedOut && state != Cancelled && state != Unknown) {
		return "", ErrRunNotRetryable
	}
	var retryID RunID
	err = tx.QueryRow(ctx, `
		INSERT INTO task_runs (
			task_definition_id, script_version_id, requested_by, trigger_type, state,
			parameters_snapshot, scheduled_for, queued_at, attempt, retry_of,
			priority, cpu_millicores, memory_bytes, disk_bytes, max_concurrency,
			timeout_seconds, max_wait_seconds, max_retries, retry_backoff_seconds,
			idempotent, required_labels, required_runtime, created_at, updated_at
		)
		SELECT task_definition_id, script_version_id, requested_by, 'retry', 'queued',
		       parameters_snapshot, NULL, $2, attempt+1, COALESCE(retry_of,id),
		       priority, cpu_millicores, memory_bytes, disk_bytes, max_concurrency,
		       timeout_seconds, max_wait_seconds, max_retries, retry_backoff_seconds,
		       idempotent, required_labels, required_runtime, $2, $2
		FROM task_runs WHERE id=$1
		RETURNING id
	`, runID, at).Scan(&retryID)
	if err != nil {
		return "", fmt.Errorf("创建重试运行实例：%w", err)
	}
	if err := appendSystemRunEvent(ctx, tx, string(retryID), "run.queued", Queued, map[string]any{"message": "重试任务已进入排队队列", "retryOf": runID}, at); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("提交任务重试：%w", err)
	}
	return retryID, nil
}

type runEventExecutor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func appendSystemRunEvent(ctx context.Context, executor runEventExecutor, runID, eventType string, state RunState, payload map[string]any, at time.Time) error {
	body, _ := json.Marshal(payload)
	_, err := executor.Exec(ctx, `
		INSERT INTO run_events (task_run_id, sequence, event_type, state, payload, occurred_at)
		SELECT $1, COALESCE(MAX(sequence),-1)+1, $2, $3, $4, $5
		FROM run_events WHERE task_run_id=$1
	`, runID, eventType, state, body, at)
	if err != nil {
		return fmt.Errorf("写入任务对账事件：%w", err)
	}
	return nil
}

var _ ReconcileStore = (*PostgresReconcileStore)(nil)

type Reconciler struct {
	store ReconcileStore
	now   func() time.Time
}

func NewReconciler(store ReconcileStore, now func() time.Time) *Reconciler {
	if now == nil {
		now = time.Now
	}
	return &Reconciler{store: store, now: now}
}

func (r *Reconciler) ServerOffline(ctx context.Context, serverID string) error {
	if r == nil || r.store == nil || strings.TrimSpace(serverID) == "" {
		return ErrInvalidRunEvent
	}
	return r.store.MarkServerRunsUnknown(ctx, serverID, r.now().UTC())
}

func (r *Reconciler) Reconcile(ctx context.Context, report agentprotocol.RunningReport) error {
	if r == nil || r.store == nil || strings.TrimSpace(report.ServerID) == "" {
		return ErrInvalidRunEvent
	}
	if report.ReportedAt.IsZero() {
		report.ReportedAt = r.now().UTC()
	}
	for _, process := range report.Processes {
		if strings.TrimSpace(process.RunID) == "" || strings.TrimSpace(process.ExecutionToken) == "" {
			return ErrInvalidRunEvent
		}
	}
	return r.store.ReconcileRunning(ctx, report, r.now().UTC())
}

func (r *Reconciler) Retry(ctx context.Context, runID string) (RunID, error) {
	if r == nil || r.store == nil || strings.TrimSpace(runID) == "" {
		return "", ErrRunNotRetryable
	}
	return r.store.RetryRun(ctx, RunID(runID), r.now().UTC())
}
