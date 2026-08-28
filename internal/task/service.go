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
)

var (
	ErrDefinitionNotFound = errors.New("任务定义不存在")
	ErrDefinitionDisabled = errors.New("任务已停用")
	ErrInvalidDefinition  = errors.New("任务定义信息不完整")
	ErrVersionUnavailable = errors.New("任务没有可执行的脚本版本")
	ErrDuplicateRun       = errors.New("该计划时刻已经创建运行实例")
)

type Service struct {
	db  *pgxpool.Pool
	now func() time.Time
}

func NewService(db *pgxpool.Pool, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{db: db, now: now}
}

func (s *Service) Create(ctx context.Context, input CreateInput) (Definition, error) {
	input = normalizeInput(input)
	if err := validateInput(input); err != nil {
		return Definition{}, err
	}
	if err := s.validateReferences(ctx, input); err != nil {
		return Definition{}, err
	}
	parameters, _ := json.Marshal(input.Parameters)
	secrets, _ := json.Marshal(input.SecretRefs)
	labels, _ := json.Marshal(input.RequiredLabels)
	row := s.db.QueryRow(ctx, `
		INSERT INTO task_definitions (
			name, description, script_id, version_policy, pinned_version_id,
			enabled, parameters, secret_bindings, required_labels,
			required_runtime, cpu_millicores, memory_bytes, disk_bytes,
			timeout_seconds, max_retries, priority, max_concurrency,
			max_wait_seconds, retry_backoff_seconds, idempotent, created_by
		)
		VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13,
			$14, $15, $16, $17, $18, $19, $20, $21
		)
		RETURNING id
	`, input.Name, input.Description, input.ScriptID, input.VersionPolicy,
		nullableUUID(input.PinnedVersionID), input.Enabled, parameters, secrets, labels,
		input.RequiredRuntime, input.Resources.CPUMillicores, input.Resources.MemoryBytes,
		input.Resources.DiskBytes, input.TimeoutSeconds, input.RetryPolicy.MaxRetries,
		input.Priority, input.MaxConcurrency, input.MaxWaitSeconds,
		input.RetryPolicy.BackoffSeconds, input.Idempotent, nullableUUID(input.CreatedBy))
	var id string
	if err := row.Scan(&id); err != nil {
		return Definition{}, fmt.Errorf("创建任务定义：%w", err)
	}
	return s.Get(ctx, id)
}

func (s *Service) Update(ctx context.Context, id string, input CreateInput) (Definition, error) {
	input = normalizeInput(input)
	if err := validateInput(input); err != nil {
		return Definition{}, err
	}
	if err := s.validateReferences(ctx, input); err != nil {
		return Definition{}, err
	}
	parameters, _ := json.Marshal(input.Parameters)
	secrets, _ := json.Marshal(input.SecretRefs)
	labels, _ := json.Marshal(input.RequiredLabels)
	command, err := s.db.Exec(ctx, `
		UPDATE task_definitions SET
			name=$2, description=$3, script_id=$4, version_policy=$5,
			pinned_version_id=$6, enabled=$7, parameters=$8, secret_bindings=$9,
			required_labels=$10, required_runtime=$11, cpu_millicores=$12,
			memory_bytes=$13, disk_bytes=$14, timeout_seconds=$15, max_retries=$16,
			priority=$17, max_concurrency=$18, max_wait_seconds=$19,
			retry_backoff_seconds=$20, idempotent=$21, updated_at=$22
		WHERE id=$1
	`, id, input.Name, input.Description, input.ScriptID, input.VersionPolicy,
		nullableUUID(input.PinnedVersionID), input.Enabled, parameters, secrets, labels,
		input.RequiredRuntime, input.Resources.CPUMillicores, input.Resources.MemoryBytes,
		input.Resources.DiskBytes, input.TimeoutSeconds, input.RetryPolicy.MaxRetries,
		input.Priority, input.MaxConcurrency, input.MaxWaitSeconds,
		input.RetryPolicy.BackoffSeconds, input.Idempotent, s.now())
	if err != nil {
		return Definition{}, fmt.Errorf("更新任务定义：%w", err)
	}
	if command.RowsAffected() == 0 {
		return Definition{}, ErrDefinitionNotFound
	}
	return s.Get(ctx, id)
}

func (s *Service) Get(ctx context.Context, id string) (Definition, error) {
	definition, err := scanDefinition(s.db.QueryRow(ctx, definitionSelect+` WHERE definition.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Definition{}, ErrDefinitionNotFound
	}
	if err != nil {
		return Definition{}, fmt.Errorf("读取任务定义：%w", err)
	}
	return definition, nil
}

func (s *Service) List(ctx context.Context) ([]Definition, error) {
	rows, err := s.db.Query(ctx, definitionSelect+` ORDER BY definition.created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("读取任务定义列表：%w", err)
	}
	defer rows.Close()
	definitions := []Definition{}
	for rows.Next() {
		definition, err := scanDefinition(rows)
		if err != nil {
			return nil, fmt.Errorf("解析任务定义：%w", err)
		}
		definitions = append(definitions, definition)
	}
	return definitions, rows.Err()
}

func (s *Service) Delete(ctx context.Context, id string) error {
	command, err := s.db.Exec(ctx, `DELETE FROM task_definitions WHERE id=$1`, id)
	if err != nil {
		return fmt.Errorf("删除任务定义：%w", err)
	}
	if command.RowsAffected() == 0 {
		return ErrDefinitionNotFound
	}
	return nil
}

func (s *Service) SetEnabled(ctx context.Context, id string, enabled, cancelQueued bool) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("开始停启任务事务：%w", err)
	}
	defer tx.Rollback(ctx)
	command, err := tx.Exec(ctx, `UPDATE task_definitions SET enabled=$2, updated_at=$3 WHERE id=$1`, id, enabled, s.now())
	if err != nil {
		return fmt.Errorf("停启任务定义：%w", err)
	}
	if command.RowsAffected() == 0 {
		return ErrDefinitionNotFound
	}
	if !enabled && cancelQueued {
		rows, err := tx.Query(ctx, `
			UPDATE task_runs
			SET state='cancelled', finished_at=$2, updated_at=$2
			WHERE task_definition_id=$1 AND state='queued'
			RETURNING id
		`, id, s.now())
		if err != nil {
			return fmt.Errorf("取消排队实例：%w", err)
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
		rows.Close()
		for _, runID := range runIDs {
			if _, err := tx.Exec(ctx, `
				INSERT INTO run_events (task_run_id, sequence, event_type, state, payload, occurred_at)
				SELECT $1, COALESCE(MAX(sequence), -1)+1, 'run.cancelled', 'cancelled',
				       '{"message":"任务停用并取消排队"}'::jsonb, $2
				FROM run_events WHERE task_run_id=$1
			`, runID, s.now()); err != nil {
				return fmt.Errorf("写入取消事件：%w", err)
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("提交停启任务事务：%w", err)
	}
	return nil
}

func (s *Service) Trigger(ctx context.Context, definitionID string, trigger Trigger) (Run, error) {
	if trigger.Type == "" {
		trigger.Type = TriggerManual
	}
	if trigger.Type != TriggerManual && trigger.Type != TriggerSchedule && trigger.Type != TriggerRetry {
		return Run{}, ErrInvalidDefinition
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Run{}, fmt.Errorf("开始创建运行实例事务：%w", err)
	}
	defer tx.Rollback(ctx)

	var policy VersionPolicy
	var pinnedVersionID *string
	var scriptID string
	var enabled, idempotent bool
	var parametersJSON, labelsJSON []byte
	var requiredRuntime string
	var priority, cpu, timeout, maxWait, maxRetries, backoff, maxConcurrency int
	var memory, disk int64
	err = tx.QueryRow(ctx, `
		SELECT script_id, version_policy, pinned_version_id, enabled, parameters,
		       required_labels, required_runtime,
		       priority, cpu_millicores, memory_bytes, disk_bytes, max_concurrency,
		       timeout_seconds, max_wait_seconds, max_retries, retry_backoff_seconds,
		       idempotent
		FROM task_definitions
		WHERE id=$1
		FOR SHARE
	`, definitionID).Scan(&scriptID, &policy, &pinnedVersionID, &enabled, &parametersJSON,
		&labelsJSON, &requiredRuntime,
		&priority, &cpu, &memory, &disk, &maxConcurrency, &timeout, &maxWait,
		&maxRetries, &backoff, &idempotent)
	if errors.Is(err, pgx.ErrNoRows) {
		return Run{}, ErrDefinitionNotFound
	}
	if err != nil {
		return Run{}, fmt.Errorf("读取任务定义：%w", err)
	}
	if !enabled {
		return Run{}, ErrDefinitionDisabled
	}
	var versionID string
	if policy == VersionPinned && pinnedVersionID != nil {
		versionID = *pinnedVersionID
	} else {
		err = tx.QueryRow(ctx, `SELECT id FROM script_versions WHERE script_id=$1 ORDER BY version DESC LIMIT 1`, scriptID).Scan(&versionID)
		if errors.Is(err, pgx.ErrNoRows) {
			return Run{}, ErrVersionUnavailable
		}
		if err != nil {
			return Run{}, fmt.Errorf("解析脚本版本：%w", err)
		}
	}
	parameters := map[string]any{}
	if err := json.Unmarshal(parametersJSON, &parameters); err != nil {
		return Run{}, fmt.Errorf("解析任务参数：%w", err)
	}
	for key, value := range trigger.Parameters {
		parameters[key] = value
	}
	requiredLabels := map[string]string{}
	if err := json.Unmarshal(labelsJSON, &requiredLabels); err != nil {
		return Run{}, fmt.Errorf("解析任务标签：%w", err)
	}
	parametersJSON, _ = json.Marshal(parameters)
	now := s.now()
	run := Run{
		DefinitionID: definitionID, ScriptVersionID: versionID, TriggerType: trigger.Type,
		State: Queued, Parameters: parameters, RequiredLabels: requiredLabels,
		RequiredRuntime: requiredRuntime, Priority: priority,
		Resources:      Resources{CPUMillicores: cpu, MemoryBytes: memory, DiskBytes: disk},
		MaxConcurrency: maxConcurrency, TimeoutSeconds: timeout, MaxWaitSeconds: maxWait,
		RetryPolicy: RetryPolicy{MaxRetries: maxRetries, BackoffSeconds: backoff},
		Idempotent:  idempotent, ScheduledFor: trigger.ScheduledFor, QueuedAt: now, CreatedAt: now,
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO task_runs (
			task_definition_id, script_version_id, requested_by, trigger_type, state,
			parameters_snapshot, required_labels, required_runtime,
			scheduled_for, queued_at, priority, cpu_millicores,
			memory_bytes, disk_bytes, max_concurrency, timeout_seconds,
			max_wait_seconds, max_retries, retry_backoff_seconds, idempotent,
			created_at, updated_at
		)
		VALUES ($1,$2,$3,$4,'queued',$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$9,$9)
		RETURNING id
	`, definitionID, versionID, nullableUUID(trigger.RequestedBy), trigger.Type,
		parametersJSON, labelsJSON, requiredRuntime, trigger.ScheduledFor, now, priority, cpu, memory, disk,
		maxConcurrency, timeout, maxWait, maxRetries, backoff, idempotent).Scan(&run.ID)
	if err != nil {
		var pgError *pgconn.PgError
		if errors.As(err, &pgError) && pgError.Code == "23505" && trigger.ScheduledFor != nil {
			return Run{}, ErrDuplicateRun
		}
		return Run{}, fmt.Errorf("创建运行实例：%w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO run_events (task_run_id, sequence, event_type, state, payload, occurred_at)
		VALUES ($1, 0, 'run.queued', 'queued', '{"message":"任务已进入排队队列"}'::jsonb, $2)
	`, run.ID, now); err != nil {
		return Run{}, fmt.Errorf("写入排队事件：%w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Run{}, fmt.Errorf("提交运行实例：%w", err)
	}
	return run, nil
}

const definitionSelect = `
	SELECT definition.id, definition.name, definition.description, definition.script_id,
	       script.name, definition.version_policy,
	       COALESCE(definition.pinned_version_id::text, ''), definition.parameters,
	       definition.secret_bindings, definition.priority, definition.required_labels,
	       definition.required_runtime, definition.cpu_millicores,
	       definition.memory_bytes, definition.disk_bytes, definition.max_concurrency,
	       definition.timeout_seconds, definition.max_wait_seconds,
	       definition.max_retries, definition.retry_backoff_seconds,
	       definition.idempotent, definition.enabled,
	       COALESCE(definition.created_by::text, ''), definition.created_at,
	       definition.updated_at
	FROM task_definitions AS definition
	JOIN scripts AS script ON script.id=definition.script_id
`

type scanner interface {
	Scan(...any) error
}

func scanDefinition(row scanner) (Definition, error) {
	var definition Definition
	var parametersJSON, secretsJSON, labelsJSON []byte
	err := row.Scan(&definition.ID, &definition.Name, &definition.Description,
		&definition.ScriptID, &definition.ScriptName, &definition.VersionPolicy,
		&definition.PinnedVersionID, &parametersJSON, &secretsJSON, &definition.Priority,
		&labelsJSON, &definition.RequiredRuntime, &definition.Resources.CPUMillicores,
		&definition.Resources.MemoryBytes, &definition.Resources.DiskBytes,
		&definition.MaxConcurrency, &definition.TimeoutSeconds, &definition.MaxWaitSeconds,
		&definition.RetryPolicy.MaxRetries, &definition.RetryPolicy.BackoffSeconds,
		&definition.Idempotent, &definition.Enabled, &definition.CreatedBy,
		&definition.CreatedAt, &definition.UpdatedAt)
	if err != nil {
		return Definition{}, err
	}
	if err := json.Unmarshal(parametersJSON, &definition.Parameters); err != nil {
		return Definition{}, err
	}
	if err := json.Unmarshal(secretsJSON, &definition.SecretRefs); err != nil {
		return Definition{}, err
	}
	if err := json.Unmarshal(labelsJSON, &definition.RequiredLabels); err != nil {
		return Definition{}, err
	}
	return definition, nil
}

func normalizeInput(input CreateInput) CreateInput {
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	input.ScriptID = strings.TrimSpace(input.ScriptID)
	input.PinnedVersionID = strings.TrimSpace(input.PinnedVersionID)
	input.RequiredRuntime = strings.ToLower(strings.TrimSpace(input.RequiredRuntime))
	if input.VersionPolicy == "" {
		input.VersionPolicy = VersionLatest
	}
	if input.Parameters == nil {
		input.Parameters = map[string]any{}
	}
	if input.SecretRefs == nil {
		input.SecretRefs = map[string]string{}
	}
	if input.RequiredLabels == nil {
		input.RequiredLabels = map[string]string{}
	}
	if input.MaxConcurrency == 0 {
		input.MaxConcurrency = 1
	}
	if input.TimeoutSeconds == 0 {
		input.TimeoutSeconds = 3600
	}
	if input.MaxWaitSeconds == 0 {
		input.MaxWaitSeconds = 86400
	}
	return input
}

func validateInput(input CreateInput) error {
	if input.Name == "" || input.ScriptID == "" || input.RequiredRuntime == "" ||
		input.Resources.CPUMillicores <= 0 || input.Resources.MemoryBytes <= 0 ||
		input.Resources.DiskBytes <= 0 || input.Priority < 0 || input.Priority > 100 ||
		input.MaxConcurrency <= 0 || input.TimeoutSeconds <= 0 || input.MaxWaitSeconds <= 0 ||
		input.RetryPolicy.MaxRetries < 0 || input.RetryPolicy.BackoffSeconds < 0 {
		return ErrInvalidDefinition
	}
	if input.VersionPolicy == VersionLatest && input.PinnedVersionID == "" {
		return nil
	}
	if input.VersionPolicy == VersionPinned && input.PinnedVersionID != "" {
		return nil
	}
	return ErrInvalidDefinition
}

func (s *Service) validateReferences(ctx context.Context, input CreateInput) error {
	var runtime string
	if err := s.db.QueryRow(ctx, `SELECT runtime FROM scripts WHERE id=$1`, input.ScriptID).Scan(&runtime); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInvalidDefinition
		}
		return fmt.Errorf("校验任务脚本：%w", err)
	}
	if strings.ToLower(runtime) != input.RequiredRuntime {
		return ErrInvalidDefinition
	}
	if input.VersionPolicy == VersionPinned {
		var exists bool
		if err := s.db.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM script_versions WHERE id=$1 AND script_id=$2
			)
		`, input.PinnedVersionID, input.ScriptID).Scan(&exists); err != nil {
			return fmt.Errorf("校验固定脚本版本：%w", err)
		}
		if !exists {
			return ErrInvalidDefinition
		}
	}
	return nil
}

func nullableUUID(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
