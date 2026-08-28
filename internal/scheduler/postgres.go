package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"yunling.local/platform/internal/server"
	"yunling.local/platform/internal/task"
)

type PostgresStore struct {
	db *pgxpool.Pool
}

func NewPostgresStore(db *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{db: db}
}

func (s *PostgresStore) Get(ctx context.Context, runID string) (task.Run, error) {
	run, err := scanRun(s.db.QueryRow(ctx, runSelect+` WHERE run.id=$1`, runID))
	if errors.Is(err, pgx.ErrNoRows) {
		return task.Run{}, ErrRunNotFound
	}
	if err != nil {
		return task.Run{}, fmt.Errorf("读取调度运行实例：%w", err)
	}
	return run, nil
}

func (s *PostgresStore) ListQueued(ctx context.Context) ([]task.Run, error) {
	rows, err := s.db.Query(ctx, runSelect+` WHERE run.state='queued' ORDER BY run.priority DESC, run.queued_at, run.id`)
	if err != nil {
		return nil, fmt.Errorf("读取排队任务：%w", err)
	}
	defer rows.Close()
	runs := []task.Run{}
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, fmt.Errorf("解析排队任务：%w", err)
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func (s *PostgresStore) CountActive(ctx context.Context, definitionID string) (int, error) {
	var count int
	if err := s.db.QueryRow(ctx, `
		SELECT count(*) FROM task_runs
		WHERE task_definition_id=$1 AND state IN ('assigned','syncing','running')
	`, definitionID).Scan(&count); err != nil {
		return 0, fmt.Errorf("统计任务并发：%w", err)
	}
	return count, nil
}

func (s *PostgresStore) Snapshots(ctx context.Context, run task.Run) ([]server.Snapshot, error) {
	rows, err := s.db.Query(ctx, `
		SELECT server.id, server.status, server.enabled, server.drain_requested,
		       server.labels, server.runtimes, server.max_concurrency,
		       server.scheduling_weight,
		       COALESCE(snapshot.cpu_total_milli,0), COALESCE(snapshot.cpu_used_milli,0),
		       COALESCE(snapshot.memory_total_bytes,0), COALESCE(snapshot.memory_available_bytes,0),
		       COALESCE(snapshot.disk_total_bytes,0), COALESCE(snapshot.disk_available_bytes,0),
		       COALESCE(snapshot.running_tasks,0), COALESCE(sync.status,''),
		       CASE WHEN recent.assigned_at IS NULL THEN 10000
		            ELSE LEAST(10000, GREATEST(0, EXTRACT(EPOCH FROM (now()-recent.assigned_at))*10000/3600))::bigint
		       END
		FROM servers AS server
		LEFT JOIN LATERAL (
			SELECT cpu_total_milli, cpu_used_milli, memory_total_bytes,
			       memory_available_bytes, disk_total_bytes, disk_available_bytes,
			       running_tasks
			FROM server_snapshots
			WHERE server_id=server.id
			ORDER BY collected_at DESC
			LIMIT 1
		) AS snapshot ON true
		LEFT JOIN script_syncs AS sync
		  ON sync.server_id=server.id AND sync.script_version_id=$1
		LEFT JOIN LATERAL (
			SELECT max(assigned_at) AS assigned_at
			FROM task_runs
			WHERE assigned_server_id=server.id
		) AS recent ON true
		ORDER BY server.id
	`, run.ScriptVersionID)
	if err != nil {
		return nil, fmt.Errorf("读取服务器调度快照：%w", err)
	}
	defer rows.Close()
	items := []server.Snapshot{}
	for rows.Next() {
		var item server.Snapshot
		var labelsJSON, runtimesJSON []byte
		var cpuTotal, cpuUsed int64
		var syncState string
		if err := rows.Scan(
			&item.ID, &item.Status, &item.Enabled, &item.Draining,
			&labelsJSON, &runtimesJSON, &item.MaxConcurrency, &item.SchedulingWeight,
			&cpuTotal, &cpuUsed, &item.MemoryTotalBytes, &item.MemoryAvailableBytes,
			&item.DiskTotalBytes, &item.DiskAvailableBytes, &item.RunningTasks,
			&syncState, &item.FairnessScore,
		); err != nil {
			return nil, fmt.Errorf("解析服务器调度快照：%w", err)
		}
		if err := json.Unmarshal(labelsJSON, &item.Labels); err != nil {
			return nil, fmt.Errorf("解析服务器标签：%w", err)
		}
		if err := json.Unmarshal(runtimesJSON, &item.Runtimes); err != nil {
			return nil, fmt.Errorf("解析服务器运行环境：%w", err)
		}
		item.CPUTotalMillicores = int(cpuTotal)
		item.CPUAvailableMillicores = int(max(cpuTotal-cpuUsed, 0))
		item.ReadyScriptVersions = map[string]bool{run.ScriptVersionID: syncState == "ready"}
		item.BlockedScriptVersions = map[string]bool{run.ScriptVersionID: syncState == "drifted"}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *PostgresStore) Assign(ctx context.Context, assignment Assignment) (bool, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("开始任务分配事务：%w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var definitionID string
	err = tx.QueryRow(ctx, `SELECT task_definition_id::text FROM task_runs WHERE id=$1`, assignment.RunID).Scan(&definitionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("读取任务并发键：%w", err)
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, definitionID); err != nil {
		return false, fmt.Errorf("锁定任务并发配额：%w", err)
	}
	var runID string
	err = tx.QueryRow(ctx, `
		UPDATE task_runs AS candidate
		SET state='assigned', assigned_server_id=$2, assigned_at=$3,
		    execution_token=$4, process_confirmed_gone=false, updated_at=$3
		WHERE candidate.id=$1 AND candidate.state='queued'
		  AND (
			SELECT count(*) FROM task_runs AS active
			WHERE active.task_definition_id=candidate.task_definition_id
			  AND active.state IN ('assigned','syncing','running')
		  ) < candidate.max_concurrency
		RETURNING id
	`, assignment.RunID, assignment.ServerID, assignment.AssignedAt, assignment.ExecutionToken).Scan(&runID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("锁定任务分配：%w", err)
	}
	lease := assignment.Lease
	if _, err := tx.Exec(ctx, `
		INSERT INTO resource_leases (
			id, task_run_id, server_id, cpu_millicores, memory_bytes,
			disk_bytes, expires_at, created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
	`, lease.ID, assignment.RunID, assignment.ServerID, lease.Resources.CPUMillicores,
		lease.Resources.MemoryBytes, lease.Resources.DiskBytes, lease.ExpiresAt,
		assignment.AssignedAt); err != nil {
		return false, fmt.Errorf("保存数据库资源租约：%w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO run_events (task_run_id, sequence, event_type, state, payload, occurred_at)
		SELECT $1, COALESCE(MAX(sequence),-1)+1, 'run.assigned', 'assigned',
		       jsonb_build_object('serverId',$2::text,'leaseId',$3::text), $4
		FROM run_events WHERE task_run_id=$1
	`, assignment.RunID, assignment.ServerID, lease.ID, assignment.AssignedAt); err != nil {
		return false, fmt.Errorf("写入任务分配事件：%w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("提交任务分配事务：%w", err)
	}
	return true, nil
}

func (s *PostgresStore) Expire(ctx context.Context, runID string, at time.Time) (bool, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("开始任务过期事务：%w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var id string
	err = tx.QueryRow(ctx, `
		UPDATE task_runs
		SET state='expired', finished_at=$2, updated_at=$2
		WHERE id=$1 AND state='queued'
		RETURNING id
	`, runID, at).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("标记排队任务过期：%w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO run_events (task_run_id, sequence, event_type, state, payload, occurred_at)
		SELECT $1, COALESCE(MAX(sequence),-1)+1, 'run.expired', 'expired',
		       '{"message":"任务超过最大等待时间"}'::jsonb, $2
		FROM run_events WHERE task_run_id=$1
	`, runID, at); err != nil {
		return false, fmt.Errorf("写入任务过期事件：%w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("提交任务过期事务：%w", err)
	}
	return true, nil
}

const runSelect = `
	SELECT run.id, run.task_definition_id, run.script_version_id, run.trigger_type,
	       run.state, run.parameters_snapshot, run.required_labels,
	       run.required_runtime, run.priority, run.cpu_millicores,
	       run.memory_bytes, run.disk_bytes, run.max_concurrency,
	       run.timeout_seconds, run.max_wait_seconds, run.max_retries,
	       run.retry_backoff_seconds, run.idempotent, run.scheduled_for,
	       run.queued_at, run.created_at
	FROM task_runs AS run
	JOIN task_definitions AS definition ON definition.id=run.task_definition_id
`

type rowScanner interface{ Scan(...any) error }

func scanRun(row rowScanner) (task.Run, error) {
	var run task.Run
	var parametersJSON, labelsJSON []byte
	err := row.Scan(
		&run.ID, &run.DefinitionID, &run.ScriptVersionID, &run.TriggerType,
		&run.State, &parametersJSON, &labelsJSON, &run.RequiredRuntime,
		&run.Priority, &run.Resources.CPUMillicores, &run.Resources.MemoryBytes,
		&run.Resources.DiskBytes, &run.MaxConcurrency, &run.TimeoutSeconds,
		&run.MaxWaitSeconds, &run.RetryPolicy.MaxRetries,
		&run.RetryPolicy.BackoffSeconds, &run.Idempotent, &run.ScheduledFor,
		&run.QueuedAt, &run.CreatedAt,
	)
	if err != nil {
		return task.Run{}, err
	}
	if err := json.Unmarshal(parametersJSON, &run.Parameters); err != nil {
		return task.Run{}, err
	}
	if err := json.Unmarshal(labelsJSON, &run.RequiredLabels); err != nil {
		return task.Run{}, err
	}
	return run, nil
}
