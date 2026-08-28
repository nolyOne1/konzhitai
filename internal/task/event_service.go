package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"yunling.local/platform/internal/agentprotocol"
)

var (
	ErrRunNotFound            = errors.New("运行实例不存在")
	ErrInvalidRunEvent        = errors.New("任务运行事件无效")
	ErrExecutionTokenMismatch = errors.New("执行令牌与当前运行不匹配")
	ErrRunEventConflict       = errors.New("任务运行事件与已保存内容冲突")
	ErrRunEventSequence       = errors.New("任务运行事件序号不连续")
)

type RunEventStore interface {
	ApplyRunEvent(ctx context.Context, event agentprotocol.RunEvent, state RunState) (applied bool, err error)
}

type PostgresRunEventStore struct{ db *pgxpool.Pool }

func NewPostgresRunEventStore(db *pgxpool.Pool) *PostgresRunEventStore {
	return &PostgresRunEventStore{db: db}
}

func (s *PostgresRunEventStore) ApplyRunEvent(ctx context.Context, event agentprotocol.RunEvent, state RunState) (bool, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("开始任务事件事务：%w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var currentToken string
	var currentState RunState
	err = tx.QueryRow(ctx, `SELECT COALESCE(execution_token,''), state FROM task_runs WHERE id=$1 FOR UPDATE`, event.RunID).Scan(&currentToken, &currentState)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrRunNotFound
	}
	if err != nil {
		return false, fmt.Errorf("锁定任务运行实例：%w", err)
	}
	if currentToken != event.ExecutionToken {
		return false, ErrExecutionTokenMismatch
	}
	var existingType string
	var existingPayload []byte
	err = tx.QueryRow(ctx, `
		SELECT event_type, payload FROM run_events
		WHERE task_run_id=$1 AND execution_token=$2 AND agent_sequence=$3
	`, event.RunID, event.ExecutionToken, event.Sequence).Scan(&existingType, &existingPayload)
	if err == nil {
		var payload struct {
			ExitCode int    `json:"exitCode"`
			Message  string `json:"message"`
		}
		if json.Unmarshal(existingPayload, &payload) != nil || existingType != "run."+event.Type || payload.ExitCode != event.ExitCode || payload.Message != event.Message {
			return false, ErrRunEventConflict
		}
		return false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return false, fmt.Errorf("检查重复任务事件：%w", err)
	}
	var nextSequence uint64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(agent_sequence),0)+1
		FROM run_events
		WHERE task_run_id=$1 AND execution_token=$2
	`, event.RunID, event.ExecutionToken).Scan(&nextSequence); err != nil {
		return false, fmt.Errorf("读取下一个任务运行事件序号：%w", err)
	}
	if event.Sequence != nextSequence {
		return false, ErrRunEventSequence
	}
	if !validAgentTransition(currentState, state) {
		return false, ErrInvalidRunEvent
	}
	payload, _ := json.Marshal(map[string]any{"message": event.Message, "exitCode": event.ExitCode})
	if _, err := tx.Exec(ctx, `
		INSERT INTO run_events (
			task_run_id, sequence, event_type, state, payload, occurred_at,
			execution_token, agent_sequence
		)
		SELECT $1, COALESCE(MAX(sequence),-1)+1, $2, $3, $4, $5, $6, $7
		FROM run_events WHERE task_run_id=$1
	`, event.RunID, "run."+event.Type, state, payload, event.OccurredAt,
		event.ExecutionToken, event.Sequence); err != nil {
		return false, fmt.Errorf("保存任务运行事件：%w", err)
	}
	finishedAt := any(nil)
	if state.Terminal() {
		finishedAt = event.OccurredAt
	}
	if _, err := tx.Exec(ctx, `
		UPDATE task_runs SET state=$2,
			started_at=CASE WHEN $2='running' THEN COALESCE(started_at,$3) ELSE started_at END,
			finished_at=COALESCE($4,finished_at),
			exit_code=CASE WHEN $4::timestamptz IS NOT NULL THEN $5 ELSE exit_code END,
			process_confirmed_gone=CASE WHEN $4::timestamptz IS NOT NULL THEN true ELSE false END,
			updated_at=$3
		WHERE id=$1
	`, event.RunID, state, event.OccurredAt, finishedAt, event.ExitCode); err != nil {
		return false, fmt.Errorf("更新任务运行状态：%w", err)
	}
	if state.Terminal() {
		if _, err := tx.Exec(ctx, `UPDATE resource_leases SET released_at=$2 WHERE task_run_id=$1 AND released_at IS NULL`, event.RunID, event.OccurredAt); err != nil {
			return false, fmt.Errorf("释放任务资源租约：%w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("提交任务运行事件：%w", err)
	}
	return true, nil
}

func validAgentTransition(from, to RunState) bool {
	if from == to {
		return true
	}
	if to == Running {
		return from == Assigned || from == Syncing || from == Unknown
	}
	return to.Terminal() && (from == Assigned || from == Syncing || from == Running || from == Unknown)
}

var _ RunEventStore = (*PostgresRunEventStore)(nil)

type EventService struct{ store RunEventStore }

func NewEventService(store RunEventStore) *EventService { return &EventService{store: store} }

func (s *EventService) Apply(ctx context.Context, event agentprotocol.RunEvent) error {
	state, ok := stateForAgentEvent(event.Type)
	if s == nil || s.store == nil || strings.TrimSpace(event.RunID) == "" ||
		strings.TrimSpace(event.ExecutionToken) == "" || event.Sequence == 0 || !ok {
		return ErrInvalidRunEvent
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now().UTC()
	}
	_, err := s.store.ApplyRunEvent(ctx, event, state)
	return err
}

func stateForAgentEvent(eventType string) (RunState, bool) {
	switch eventType {
	case "started":
		return Running, true
	case "succeeded":
		return Succeeded, true
	case "failed":
		return Failed, true
	case "timed_out":
		return TimedOut, true
	case "cancelled":
		return Cancelled, true
	default:
		return "", false
	}
}
