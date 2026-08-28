package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"yunling.local/platform/internal/agentprotocol"
)

var (
	ErrRunNotCancellable     = errors.New("当前运行状态不能取消")
	ErrRunCommandUnavailable = errors.New("执行服务器当前无法接收取消命令")
)

type RunView struct {
	ID                   string         `json:"id"`
	DefinitionID         string         `json:"definitionId"`
	TaskName             string         `json:"taskName"`
	ScriptID             string         `json:"scriptId"`
	ScriptName           string         `json:"scriptName"`
	ScriptVersionID      string         `json:"scriptVersionId"`
	VersionNumber        int            `json:"versionNumber"`
	ServerID             string         `json:"serverId,omitempty"`
	ServerName           string         `json:"serverName,omitempty"`
	TriggerType          TriggerType    `json:"triggerType"`
	State                RunState       `json:"state"`
	Parameters           map[string]any `json:"parameters"`
	Resources            Resources      `json:"resources"`
	RequiredRuntime      string         `json:"requiredRuntime"`
	Priority             int            `json:"priority"`
	Attempt              int            `json:"attempt"`
	MaxRetries           int            `json:"maxRetries"`
	Idempotent           bool           `json:"idempotent"`
	ProcessConfirmedGone bool           `json:"processConfirmedGone"`
	QueuedAt             time.Time      `json:"queuedAt"`
	AssignedAt           *time.Time     `json:"assignedAt,omitempty"`
	StartedAt            *time.Time     `json:"startedAt,omitempty"`
	FinishedAt           *time.Time     `json:"finishedAt,omitempty"`
	ExitCode             *int           `json:"exitCode,omitempty"`
	ResultSummary        string         `json:"resultSummary"`
	CreatedAt            time.Time      `json:"createdAt"`
}

type RunStreamEvent struct {
	ID         string    `json:"id"`
	Kind       string    `json:"kind"`
	State      RunState  `json:"state,omitempty"`
	EventType  string    `json:"eventType,omitempty"`
	Stream     string    `json:"stream,omitempty"`
	Sequence   uint64    `json:"sequence"`
	Message    string    `json:"message,omitempty"`
	Content    string    `json:"content,omitempty"`
	ExitCode   *int      `json:"exitCode,omitempty"`
	OccurredAt time.Time `json:"occurredAt"`
}

type RunManager interface {
	ListRuns(context.Context) ([]RunView, error)
	GetRun(context.Context, string) (RunView, error)
	ListRunEvents(context.Context, string) ([]RunStreamEvent, error)
	CancelRun(context.Context, string) error
	RetryRun(context.Context, string) (RunID, error)
}

type ExecutionCommandSender interface {
	SendExecutionCommand(ctx context.Context, serverID string, command agentprotocol.ExecutionCommand) error
}

type runRetrier interface {
	Retry(context.Context, string) (RunID, error)
}

type RunService struct {
	db         *pgxpool.Pool
	commands   ExecutionCommandSender
	reconciler runRetrier
	now        func() time.Time
}

func NewRunService(db *pgxpool.Pool, commands ExecutionCommandSender, reconciler runRetrier, now func() time.Time) *RunService {
	if now == nil {
		now = time.Now
	}
	if reconciler == nil && db != nil {
		reconciler = NewReconciler(NewPostgresReconcileStore(db), now)
	}
	return &RunService{db: db, commands: commands, reconciler: reconciler, now: now}
}

func (s *RunService) ListRuns(ctx context.Context) ([]RunView, error) {
	rows, err := s.db.Query(ctx, runViewSelect+` ORDER BY run.created_at DESC LIMIT 200`)
	if err != nil {
		return nil, fmt.Errorf("读取执行记录：%w", err)
	}
	defer rows.Close()
	runs := []RunView{}
	for rows.Next() {
		run, err := scanRunView(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func (s *RunService) GetRun(ctx context.Context, id string) (RunView, error) {
	run, err := scanRunView(s.db.QueryRow(ctx, runViewSelect+` WHERE run.id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return RunView{}, ErrRunNotFound
	}
	if err != nil {
		return RunView{}, fmt.Errorf("读取执行详情：%w", err)
	}
	return run, nil
}

func (s *RunService) ListRunEvents(ctx context.Context, id string) ([]RunStreamEvent, error) {
	var exists bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM task_runs WHERE id=$1)`, id).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrRunNotFound
	}
	events := []RunStreamEvent{}
	rows, err := s.db.Query(ctx, `
		SELECT sequence, event_type, COALESCE(state,''), payload, occurred_at
		FROM run_events WHERE task_run_id=$1 ORDER BY sequence
	`, id)
	if err != nil {
		return nil, fmt.Errorf("读取运行状态事件：%w", err)
	}
	for rows.Next() {
		var event RunStreamEvent
		var payload []byte
		if err := rows.Scan(&event.Sequence, &event.EventType, &event.State, &payload, &event.OccurredAt); err != nil {
			rows.Close()
			return nil, err
		}
		var detail struct {
			Message  string `json:"message"`
			ExitCode *int   `json:"exitCode"`
		}
		_ = json.Unmarshal(payload, &detail)
		event.ID = fmt.Sprintf("state:%020d", event.Sequence)
		event.Kind = "state"
		event.Message = detail.Message
		event.ExitCode = detail.ExitCode
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	rows, err = s.db.Query(ctx, `
		SELECT stream, sequence, content, created_at
		FROM log_chunks WHERE task_run_id=$1 ORDER BY created_at, stream, sequence
	`, id)
	if err != nil {
		return nil, fmt.Errorf("读取实时日志：%w", err)
	}
	for rows.Next() {
		var event RunStreamEvent
		if err := rows.Scan(&event.Stream, &event.Sequence, &event.Content, &event.OccurredAt); err != nil {
			rows.Close()
			return nil, err
		}
		event.ID = fmt.Sprintf("log:%s:%020d", event.Stream, event.Sequence)
		event.Kind = "log"
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].OccurredAt.Equal(events[j].OccurredAt) {
			return events[i].ID < events[j].ID
		}
		return events[i].OccurredAt.Before(events[j].OccurredAt)
	})
	return events, nil
}

func (s *RunService) CancelRun(ctx context.Context, id string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var state RunState
	var serverID, token string
	err = tx.QueryRow(ctx, `
		SELECT state, COALESCE(assigned_server_id::text,''), COALESCE(execution_token,'')
		FROM task_runs WHERE id=$1 FOR UPDATE
	`, id).Scan(&state, &serverID, &token)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrRunNotFound
	}
	if err != nil {
		return err
	}
	now := s.now().UTC()
	if state == Queued {
		if _, err := tx.Exec(ctx, `UPDATE task_runs SET state='cancelled', finished_at=$2, process_confirmed_gone=true, updated_at=$2 WHERE id=$1`, id, now); err != nil {
			return err
		}
		if err := appendSystemRunEvent(ctx, tx, id, "run.cancelled", Cancelled, map[string]any{"message": "排队任务已取消"}, now); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if state != Assigned && state != Syncing && state != Running {
		return ErrRunNotCancellable
	}
	if s.commands == nil || serverID == "" || token == "" {
		return ErrRunCommandUnavailable
	}
	if err := s.commands.SendExecutionCommand(ctx, serverID, agentprotocol.ExecutionCommand{
		Type:         agentprotocol.CommandCancel,
		Cancellation: &agentprotocol.CancelCommand{RunID: id, ExecutionToken: token},
	}); err != nil {
		return ErrRunCommandUnavailable
	}
	if err := appendSystemRunEvent(ctx, tx, id, "run.cancel_requested", state, map[string]any{"message": "已向执行服务器发送取消命令"}, now); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *RunService) RetryRun(ctx context.Context, id string) (RunID, error) {
	if s.reconciler == nil {
		return "", ErrRunNotRetryable
	}
	return s.reconciler.Retry(ctx, id)
}

const runViewSelect = `
	SELECT run.id, definition.id, definition.name, script.id, script.name,
	       version.id, version.version, COALESCE(server.id::text,''), COALESCE(server.name,''),
	       run.trigger_type, run.state, run.parameters_snapshot,
	       run.cpu_millicores, run.memory_bytes, run.disk_bytes,
	       run.required_runtime, run.priority, run.attempt, run.max_retries, run.idempotent,
	       run.process_confirmed_gone, run.queued_at,
	       run.assigned_at, run.started_at, run.finished_at, run.exit_code,
	       run.result_summary, run.created_at
	FROM task_runs AS run
	JOIN task_definitions AS definition ON definition.id=run.task_definition_id
	JOIN scripts AS script ON script.id=definition.script_id
	JOIN script_versions AS version ON version.id=run.script_version_id
	LEFT JOIN servers AS server ON server.id=run.assigned_server_id
`

func scanRunView(row scanner) (RunView, error) {
	var run RunView
	var parameters []byte
	err := row.Scan(
		&run.ID, &run.DefinitionID, &run.TaskName, &run.ScriptID, &run.ScriptName,
		&run.ScriptVersionID, &run.VersionNumber, &run.ServerID, &run.ServerName,
		&run.TriggerType, &run.State, &parameters,
		&run.Resources.CPUMillicores, &run.Resources.MemoryBytes, &run.Resources.DiskBytes,
		&run.RequiredRuntime, &run.Priority, &run.Attempt, &run.MaxRetries, &run.Idempotent,
		&run.ProcessConfirmedGone, &run.QueuedAt,
		&run.AssignedAt, &run.StartedAt, &run.FinishedAt, &run.ExitCode,
		&run.ResultSummary, &run.CreatedAt,
	)
	if err != nil {
		return RunView{}, err
	}
	if err := json.Unmarshal(parameters, &run.Parameters); err != nil {
		return RunView{}, err
	}
	return run, nil
}

var _ RunManager = (*RunService)(nil)
