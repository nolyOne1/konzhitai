package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"
)

const maxDispatchErrorRunes = 1000

type PostgresStore struct{ db *pgxpool.Pool }

func NewPostgresStore(db *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{db: db}
}

func (s *PostgresStore) Claim(ctx context.Context, cutoff, now time.Time, limit int) ([]Run, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("派发数据库不可用")
	}
	if limit <= 0 {
		return []Run{}, nil
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("开始派发领取事务：%w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `
		WITH candidates AS (
			SELECT id
			FROM task_runs
			WHERE state='assigned'
			  AND execution_token IS NOT NULL
			  AND assigned_server_id IS NOT NULL
			  AND (last_dispatch_at IS NULL OR last_dispatch_at <= $1)
			ORDER BY assigned_at NULLS FIRST, created_at, id
			FOR UPDATE SKIP LOCKED
			LIMIT $2
		), claimed AS (
			UPDATE task_runs AS run
			SET dispatch_attempts=run.dispatch_attempts+1,
			    last_dispatch_at=$3,
			    dispatch_error='',
			    updated_at=$3
			FROM candidates
			WHERE run.id=candidates.id
			RETURNING run.*
		)
		SELECT claimed.id::text,
		       claimed.execution_token,
		       claimed.assigned_server_id::text,
		       version.script_id::text,
		       claimed.script_version_id::text,
		       claimed.required_runtime,
		       version.entrypoint,
		       claimed.parameters_snapshot,
		       definition.secret_bindings,
		       claimed.cpu_millicores,
		       claimed.memory_bytes,
		       claimed.disk_bytes,
		       claimed.timeout_seconds,
		       claimed.dispatch_attempts
		FROM claimed
		JOIN task_definitions AS definition ON definition.id=claimed.task_definition_id
		JOIN script_versions AS version ON version.id=claimed.script_version_id
		ORDER BY claimed.assigned_at NULLS FIRST, claimed.created_at, claimed.id
	`, cutoff, limit, now)
	if err != nil {
		return nil, fmt.Errorf("领取待派发运行：%w", err)
	}
	defer rows.Close()
	runs := make([]Run, 0)
	for rows.Next() {
		var run Run
		var parametersJSON, secretsJSON []byte
		var timeoutSeconds int
		if err := rows.Scan(
			&run.ID,
			&run.ExecutionToken,
			&run.ServerID,
			&run.ScriptID,
			&run.ScriptVersionID,
			&run.Runtime,
			&run.Entrypoint,
			&parametersJSON,
			&secretsJSON,
			&run.Resources.CPUMillicores,
			&run.Resources.MemoryBytes,
			&run.Resources.DiskBytes,
			&timeoutSeconds,
			&run.Attempt,
		); err != nil {
			return nil, fmt.Errorf("解析待派发运行：%w", err)
		}
		if err := json.Unmarshal(parametersJSON, &run.Parameters); err != nil {
			return nil, fmt.Errorf("解析运行参数快照：%w", err)
		}
		if err := json.Unmarshal(secretsJSON, &run.SecretBindings); err != nil {
			return nil, fmt.Errorf("解析敏感参数绑定：%w", err)
		}
		if run.Parameters == nil {
			run.Parameters = map[string]any{}
		}
		if run.SecretBindings == nil {
			run.SecretBindings = map[string]string{}
		}
		run.Timeout = time.Duration(timeoutSeconds) * time.Second
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取待派发运行：%w", err)
	}
	rows.Close()
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("提交派发领取事务：%w", err)
	}
	return runs, nil
}

func (s *PostgresStore) RecordResult(ctx context.Context, runID, executionToken, dispatchError string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("派发数据库不可用")
	}
	message := truncateRunes(dispatchError, maxDispatchErrorRunes)
	_, err := s.db.Exec(ctx, `
		UPDATE task_runs
		SET dispatch_error=$3, updated_at=now()
		WHERE id=$1 AND execution_token=$2 AND state='assigned'
	`, runID, executionToken, message)
	if err != nil {
		return fmt.Errorf("记录任务派发结果：%w", err)
	}
	return nil
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 || utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit])
}

var _ Store = (*PostgresStore)(nil)
