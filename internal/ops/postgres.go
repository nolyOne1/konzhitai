package ops

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresRepository struct{ db *pgxpool.Pool }

func NewPostgresRepository(db *pgxpool.Pool) *PostgresRepository {
	return &PostgresRepository{db: db}
}

func (r *PostgresRepository) Snapshot(ctx context.Context, now time.Time) (RuleSnapshot, error) {
	if r == nil || r.db == nil {
		return RuleSnapshot{}, fmt.Errorf("运维规则数据库尚未配置")
	}
	snapshot := RuleSnapshot{Servers: []ServerObservation{}, Runs: []RunObservation{}}
	rows, err := r.db.Query(ctx, `
		SELECT server.id::text, server.name, server.enabled, server.drain_requested,
		       server.last_seen_at
		FROM servers AS server
		WHERE server.enabled=true OR EXISTS (
			SELECT 1 FROM alert_rule_states AS state
			WHERE state.source_type='server' AND state.source_id=server.id::text
		)
		ORDER BY server.id
	`)
	if err != nil {
		return RuleSnapshot{}, fmt.Errorf("读取运维服务器：%w", err)
	}
	serverIndexes := map[string]int{}
	serverIDs := []string{}
	for rows.Next() {
		var server ServerObservation
		if err := rows.Scan(&server.ID, &server.Name, &server.Enabled, &server.Draining, &server.LastSeenAt); err != nil {
			rows.Close()
			return RuleSnapshot{}, fmt.Errorf("解析运维服务器：%w", err)
		}
		serverIndexes[server.ID] = len(snapshot.Servers)
		serverIDs = append(serverIDs, server.ID)
		snapshot.Servers = append(snapshot.Servers, server)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return RuleSnapshot{}, fmt.Errorf("遍历运维服务器：%w", err)
	}
	rows.Close()

	if len(serverIDs) > 0 {
		rows, err = r.db.Query(ctx, `
			SELECT selected.server_id::text, selected.memory_total_bytes,
			       selected.memory_available_bytes, selected.disk_total_bytes,
			       selected.disk_available_bytes, selected.collected_at
			FROM unnest($1::uuid[]) AS requested(server_id)
			CROSS JOIN LATERAL (
				SELECT snapshot.server_id, snapshot.memory_total_bytes,
				       snapshot.memory_available_bytes, snapshot.disk_total_bytes,
				       snapshot.disk_available_bytes, snapshot.collected_at
				FROM server_snapshots AS snapshot
				WHERE snapshot.server_id=requested.server_id
				ORDER BY snapshot.collected_at DESC
				LIMIT 2
			) AS selected
			ORDER BY selected.server_id, selected.collected_at
		`, serverIDs)
		if err != nil {
			return RuleSnapshot{}, fmt.Errorf("读取服务器资源快照：%w", err)
		}
		for rows.Next() {
			var serverID string
			var sample ResourceSample
			if err := rows.Scan(
				&serverID, &sample.MemoryTotalBytes, &sample.MemoryAvailableBytes,
				&sample.DiskTotalBytes, &sample.DiskAvailableBytes, &sample.CollectedAt,
			); err != nil {
				rows.Close()
				return RuleSnapshot{}, fmt.Errorf("解析服务器资源快照：%w", err)
			}
			index := serverIndexes[serverID]
			snapshot.Servers[index].Samples = append(snapshot.Servers[index].Samples, sample)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return RuleSnapshot{}, fmt.Errorf("遍历服务器资源快照：%w", err)
		}
		rows.Close()
	}

	rows, err = r.db.Query(ctx, `
		SELECT run.id::text, definition.name, run.state, run.queued_at
		FROM task_runs AS run
		JOIN task_definitions AS definition ON definition.id=run.task_definition_id
		WHERE (run.state='queued' AND run.queued_at <= $1)
		   OR (run.state IN ('failed', 'timed_out') AND NOT EXISTS (
			SELECT 1 FROM alert_rule_states AS state
			WHERE state.code='task_failed' AND state.source_type='run'
			  AND state.source_id=run.id::text
		   ))
		   OR EXISTS (
			SELECT 1 FROM alert_rule_states AS state
			WHERE state.source_type='run' AND state.source_id=run.id::text
		   )
		ORDER BY run.id
	`, now.Add(-queueThreshold))
	if err != nil {
		return RuleSnapshot{}, fmt.Errorf("读取运维任务运行：%w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var run RunObservation
		if err := rows.Scan(&run.ID, &run.TaskName, &run.State, &run.QueuedAt); err != nil {
			return RuleSnapshot{}, fmt.Errorf("解析运维任务运行：%w", err)
		}
		snapshot.Runs = append(snapshot.Runs, run)
	}
	if err := rows.Err(); err != nil {
		return RuleSnapshot{}, fmt.Errorf("遍历运维任务运行：%w", err)
	}
	return snapshot, nil
}

func (r *PostgresRepository) Apply(ctx context.Context, evaluations []Evaluation) ([]Transition, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("运维规则数据库尚未配置")
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("开始运维规则事务：%w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	states := make(map[RuleKey]RuleState, len(evaluations))
	latestEvaluations := make(map[RuleKey]Evaluation, len(evaluations))
	order := make([]RuleKey, 0, len(evaluations))
	for _, evaluation := range evaluations {
		if _, exists := latestEvaluations[evaluation.Key]; !exists {
			order = append(order, evaluation.Key)
		}
		state, loaded := states[evaluation.Key]
		if !loaded {
			state, err = loadRuleState(ctx, tx, evaluation)
			if err != nil {
				return nil, err
			}
		}
		if evaluation.SampleBased && !evaluation.EvaluatedAt.After(state.LastEvaluatedAt) {
			states[evaluation.Key] = state
			latestEvaluations[evaluation.Key] = evaluation
			continue
		}
		state.LastEvaluatedAt = evaluation.EvaluatedAt
		state.LastValue = evaluation.Value
		switch {
		case evaluation.Bad:
			state.ConsecutiveBad++
			state.ConsecutiveGood = 0
			if state.ConsecutiveBad >= evaluation.RequiredConsecutive {
				state.DesiredActive = true
			}
		case evaluation.Good:
			state.ConsecutiveGood++
			state.ConsecutiveBad = 0
			if state.ConsecutiveGood >= evaluation.RequiredConsecutive {
				state.DesiredActive = false
			}
		default:
			state.ConsecutiveBad, state.ConsecutiveGood = 0, 0
		}
		if _, err := tx.Exec(ctx, `
			UPDATE alert_rule_states SET
				desired_active=$4, consecutive_bad=$5, consecutive_good=$6,
				last_value=$7, last_evaluated_at=$8
			WHERE code=$1 AND source_type=$2 AND source_id=$3
		`, evaluation.Key.Code, evaluation.Key.SourceType, evaluation.Key.SourceID,
			state.DesiredActive, state.ConsecutiveBad, state.ConsecutiveGood,
			state.LastValue, state.LastEvaluatedAt,
		); err != nil {
			return nil, fmt.Errorf("保存运维规则状态：%w", err)
		}
		states[evaluation.Key] = state
		latestEvaluations[evaluation.Key] = evaluation
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("提交运维规则状态：%w", err)
	}
	transitions := make([]Transition, 0, len(states))
	for _, key := range order {
		state := states[key]
		if state.DesiredActive != state.Active {
			transitions = append(transitions, Transition{
				Evaluation: latestEvaluations[key], DesiredActive: state.DesiredActive,
			})
		}
	}
	return transitions, nil
}

func loadRuleState(ctx context.Context, tx pgx.Tx, evaluation Evaluation) (RuleState, error) {
	if _, err := tx.Exec(ctx, `
		INSERT INTO alert_rule_states (
			code, source_type, source_id, last_evaluated_at
		) VALUES ($1, $2, $3, 'epoch'::timestamptz)
		ON CONFLICT (code, source_type, source_id) DO NOTHING
	`, evaluation.Key.Code, evaluation.Key.SourceType, evaluation.Key.SourceID); err != nil {
		return RuleState{}, fmt.Errorf("创建运维规则状态：%w", err)
	}
	var state RuleState
	if err := tx.QueryRow(ctx, `
		SELECT active, desired_active, consecutive_bad, consecutive_good,
		       last_value, last_evaluated_at
		FROM alert_rule_states
		WHERE code=$1 AND source_type=$2 AND source_id=$3
		FOR UPDATE
	`, evaluation.Key.Code, evaluation.Key.SourceType, evaluation.Key.SourceID).Scan(
		&state.Active, &state.DesiredActive, &state.ConsecutiveBad,
		&state.ConsecutiveGood, &state.LastValue, &state.LastEvaluatedAt,
	); err != nil {
		return RuleState{}, fmt.Errorf("锁定运维规则状态：%w", err)
	}
	return state, nil
}

func (r *PostgresRepository) MarkApplied(ctx context.Context, key RuleKey, desired bool) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("运维规则数据库尚未配置")
	}
	_, err := r.db.Exec(ctx, `
		UPDATE alert_rule_states SET active=$4
		WHERE code=$1 AND source_type=$2 AND source_id=$3
		  AND desired_active=$4
	`, key.Code, key.SourceType, key.SourceID, desired)
	if err != nil {
		return fmt.Errorf("确认运维告警状态：%w", err)
	}
	return nil
}

var _ RuleRepository = (*PostgresRepository)(nil)
