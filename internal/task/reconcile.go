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
	usages := make(map[string]*agentprotocol.ResourceUsage, len(report.Processes))
	for _, process := range report.Processes {
		active[process.RunID] = process.ExecutionToken
		usages[process.RunID] = process.Usage
	}
	rows, err := tx.Query(ctx, `
		SELECT id::text, COALESCE(execution_token,''), state, process_confirmed_gone
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
		gone      bool
	}
	var candidates []candidate
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.id, &item.token, &item.state, &item.gone); err != nil {
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
			if err := saveRunUsage(ctx, tx, item.id, usages[item.id]); err != nil {
				return err
			}
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
		if _, err := tx.Exec(ctx, `UPDATE resource_leases SET released_at=$2 WHERE task_run_id=$1 AND released_at IS NULL`, item.id, at); err != nil {
			return fmt.Errorf("释放已确认消失进程的租约：%w", err)
		}
		// Repeated absence reports must not postpone the retry backoff clock.
		if item.gone {
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
	return s.retryRun(ctx, runID, at, false)
}

func (s *PostgresReconcileStore) retryRun(ctx context.Context, runID RunID, at time.Time, automatic bool) (RunID, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("开始任务重试事务：%w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Serialize the whole retry lineage, not only one parent row. retry_of
	// points at the root, so requests for an ancestor and a descendant share
	// the same lock. Take this before row locks to keep lock ordering stable.
	var rootID string
	err = tx.QueryRow(ctx, `SELECT COALESCE(retry_of,id)::text FROM task_runs WHERE id=$1`, runID).Scan(&rootID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrRunNotFound
	}
	if err != nil {
		return "", fmt.Errorf("读取任务重试链：%w", err)
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "task-retry:"+rootID); err != nil {
		return "", fmt.Errorf("锁定任务重试链：%w", err)
	}
	// All new attempts honor task disablement. Match SetEnabled's lock order:
	// definition before run, so disable-and-cancel serializes with creation.
	var enabled bool
	if err := tx.QueryRow(ctx, `SELECT enabled FROM task_definitions
		WHERE id=(SELECT task_definition_id FROM task_runs WHERE id=$1)
		FOR SHARE`, runID).Scan(&enabled); err != nil {
		return "", err
	}
	if !enabled {
		return "", ErrRunNotRetryable
	}
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
	if automatic {
		var eligible bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM run_events
			WHERE task_run_id=$1 AND payload @> '{"automaticRetry":true}'::jsonb
			AND event_type IN ('run.failed','run.timed_out'))
			AND NOT EXISTS(SELECT 1 FROM run_events WHERE task_run_id=$1 AND event_type='run.cancel_requested')`, runID).Scan(&eligible); err != nil {
			return "", err
		}
		if !eligible || (state != Failed && state != TimedOut) {
			return "", ErrRunNotRetryable
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE resource_leases SET released_at=$2 WHERE task_run_id=$1 AND released_at IS NULL`, runID, at); err != nil {
		return "", fmt.Errorf("释放重试前的旧租约：%w", err)
	}
	var retryID RunID
	// Replaying an accepted request returns its existing successor, including
	// when that successor has already completed. Never fork an older attempt.
	err = tx.QueryRow(ctx, `
		SELECT id FROM task_runs WHERE retry_of=$1 AND attempt>$2
		ORDER BY attempt, created_at, id LIMIT 1
	`, rootID, attempt).Scan(&retryID)
	if err == nil {
		return retryID, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("读取已有重试实例：%w", err)
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO task_runs (
			task_definition_id, script_version_id, requested_by, trigger_type, state,
			parameters_snapshot, scheduled_for, queued_at, attempt, retry_of,
			priority, cpu_millicores, memory_bytes, disk_bytes, max_concurrency,
			timeout_seconds, max_wait_seconds, max_retries, retry_backoff_seconds,
			idempotent, required_labels, required_runtime, created_at, updated_at
		)
		SELECT task_definition_id, script_version_id, requested_by, 'retry', 'queued',
		       parameters_snapshot, NULL,
		       GREATEST($2::timestamptz, COALESCE(finished_at,updated_at) + retry_backoff_seconds * interval '1 second'),
		       attempt+1, COALESCE(retry_of,id),
		       priority, cpu_millicores, memory_bytes, disk_bytes, max_concurrency,
		       timeout_seconds, max_wait_seconds, max_retries, retry_backoff_seconds,
		       idempotent, required_labels, required_runtime, $2, $2
		FROM task_runs WHERE id=$1
		RETURNING id
	`, runID, at).Scan(&retryID)
	if err != nil {
		return "", fmt.Errorf("创建重试运行实例：%w", err)
	}
	// Retry the same secret references that were validated for the parent run.
	// Legacy runs have no snapshot, so capture their current bindings once.
	var secretRefs json.RawMessage
	if err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT event.payload->'secretRefs'
		FROM run_events AS event WHERE event.task_run_id=run.id AND event.event_type='run.queued'
		ORDER BY event.sequence LIMIT 1), definition.secret_bindings)
		FROM task_runs AS run JOIN task_definitions AS definition ON definition.id=run.task_definition_id
		WHERE run.id=$1`, runID).Scan(&secretRefs); err != nil {
		return "", fmt.Errorf("读取重试敏感参数引用快照：%w", err)
	}
	if err := appendSystemRunEvent(ctx, tx, string(retryID), "run.queued", Queued, map[string]any{"message": "重试任务已进入排队队列", "retryOf": runID, "secretRefs": secretRefs}, at); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("提交任务重试：%w", err)
	}
	return retryID, nil
}

// RetryFailed consumes durable intent recorded with new terminal events. Old
// failures without this marker are intentionally not replayed on deployment.
// No in-memory cursor is needed: a successor is the durable acknowledgement.
func (s *PostgresReconcileStore) RetryFailed(ctx context.Context, at time.Time) error {
	rows, err := s.db.Query(ctx, `SELECT run.id FROM task_runs AS run
		JOIN task_definitions AS definition ON definition.id=run.task_definition_id
		WHERE run.state IN ('failed','timed_out') AND run.idempotent
		AND run.process_confirmed_gone AND run.attempt<=run.max_retries AND definition.enabled
		AND EXISTS (SELECT 1 FROM run_events AS event WHERE event.task_run_id=run.id
		  AND event.event_type IN ('run.failed','run.timed_out') AND event.payload @> '{"automaticRetry":true}'::jsonb)
		AND NOT EXISTS (SELECT 1 FROM run_events AS event WHERE event.task_run_id=run.id AND event.event_type='run.cancel_requested')
		AND NOT EXISTS (SELECT 1 FROM task_runs AS child
		  WHERE child.retry_of=COALESCE(run.retry_of,run.id) AND child.attempt>run.attempt)
		ORDER BY run.finished_at,run.id LIMIT 100`)
	if err != nil {
		return fmt.Errorf("读取自动重试意图：%w", err)
	}
	var ids []RunID
	for rows.Next() {
		var id RunID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := s.retryRun(ctx, id, at, true); err != nil && !errors.Is(err, ErrRunNotRetryable) && !errors.Is(err, ErrRunNotFound) {
			return fmt.Errorf("自动重试 %s：%w", id, err)
		}
	}
	return nil
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
