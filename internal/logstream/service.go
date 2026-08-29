package logstream

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const DefaultChunkSize = 64 << 10

type Stream string

const (
	StreamStdout Stream = "stdout"
	StreamStderr Stream = "stderr"
	StreamSystem Stream = "system"
)

var (
	ErrInvalidChunk  = errors.New("日志块内容无效")
	ErrSequenceGap   = errors.New("日志序号不连续")
	ErrChunkConflict = errors.New("日志序号与已保存内容冲突")
)

type LogChunk struct {
	RunID          string    `json:"runId"`
	ExecutionToken string    `json:"executionToken"`
	Sequence       uint64    `json:"sequence"`
	Stream         Stream    `json:"stream"`
	Content        string    `json:"content"`
	CreatedAt      time.Time `json:"createdAt"`
}

type PostgresChunkStore struct{ db *pgxpool.Pool }

func NewPostgresChunkStore(db *pgxpool.Pool) *PostgresChunkStore {
	return &PostgresChunkStore{db: db}
}

func (s *PostgresChunkStore) NextSequence(ctx context.Context, runID, token string, stream Stream) (uint64, error) {
	var next uint64
	err := s.db.QueryRow(ctx, `
		SELECT COALESCE(MAX(sequence), 0) + 1
		FROM log_chunks
		WHERE task_run_id=$1 AND execution_token=$2 AND stream=$3
	`, runID, token, stream).Scan(&next)
	if err != nil {
		return 0, fmt.Errorf("读取下一个日志序号：%w", err)
	}
	return next, nil
}

func (s *PostgresChunkStore) Find(ctx context.Context, chunk LogChunk) (LogChunk, bool, error) {
	var stored LogChunk
	err := s.db.QueryRow(ctx, `
		SELECT task_run_id::text, execution_token, sequence, stream, content, created_at
		FROM log_chunks
		WHERE task_run_id=$1 AND execution_token=$2 AND stream=$3 AND sequence=$4
	`, chunk.RunID, chunk.ExecutionToken, chunk.Stream, chunk.Sequence).Scan(
		&stored.RunID, &stored.ExecutionToken, &stored.Sequence, &stored.Stream,
		&stored.Content, &stored.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return LogChunk{}, false, nil
	}
	if err != nil {
		return LogChunk{}, false, fmt.Errorf("读取日志块：%w", err)
	}
	return stored, true, nil
}

func (s *PostgresChunkStore) Insert(ctx context.Context, chunk LogChunk) error {
	command, err := s.db.Exec(ctx, `
		INSERT INTO log_chunks (
			task_run_id, execution_token, stream, sequence, content, byte_size, created_at
		)
		SELECT run.id, $2, $3, $4, $5, $6, $7
		FROM task_runs AS run
		WHERE run.id=$1 AND run.execution_token=$2
	`, chunk.RunID, chunk.ExecutionToken, chunk.Stream, chunk.Sequence,
		chunk.Content, len(chunk.Content), chunk.CreatedAt)
	if err != nil {
		return fmt.Errorf("保存日志块：%w", err)
	}
	if command.RowsAffected() == 0 {
		return errors.New("日志执行令牌与运行实例不匹配")
	}
	return nil
}

var _ ChunkStore = (*PostgresChunkStore)(nil)

type ChunkStore interface {
	NextSequence(ctx context.Context, runID, executionToken string, stream Stream) (uint64, error)
	Find(ctx context.Context, chunk LogChunk) (stored LogChunk, found bool, err error)
	Insert(ctx context.Context, chunk LogChunk) error
}

type ContentRedactor interface {
	Mask(text []byte, values [][]byte) []byte
}

type SensitiveValueSource interface {
	ValuesForRun(ctx context.Context, runID, executionToken string) ([][]byte, error)
}

type ServiceOption func(*Service)

func WithRedaction(redactor ContentRedactor, values SensitiveValueSource) ServiceOption {
	return func(service *Service) {
		service.redactor = redactor
		service.values = values
	}
}

type Service struct {
	store    ChunkStore
	redactor ContentRedactor
	values   SensitiveValueSource
	mu       sync.Mutex
}

func NewService(store ChunkStore, options ...ServiceOption) *Service {
	service := &Service{store: store}
	for _, option := range options {
		option(service)
	}
	return service
}

func (s *Service) Accept(ctx context.Context, chunk LogChunk) (uint64, error) {
	if s == nil || s.store == nil || !validChunk(chunk) {
		return 0, ErrInvalidChunk
	}
	if chunk.CreatedAt.IsZero() {
		chunk.CreatedAt = time.Now().UTC()
	}
	if s.redactor != nil && s.values != nil {
		values, err := s.values.ValuesForRun(ctx, chunk.RunID, chunk.ExecutionToken)
		if err != nil {
			return 0, fmt.Errorf("读取任务敏感值用于日志脱敏：%w", err)
		}
		chunk.Content = string(s.redactor.Mask([]byte(chunk.Content), values))
		for _, value := range values {
			clear(value)
		}
	}

	// NextSequence + Insert 必须作为一个临界区执行，避免同一进程内的并发接收
	// 将相同序号写入两次。PostgreSQL 存储仍以唯一约束作为最终保护。
	s.mu.Lock()
	defer s.mu.Unlock()
	next, err := s.store.NextSequence(ctx, chunk.RunID, chunk.ExecutionToken, chunk.Stream)
	if err != nil {
		return 0, err
	}
	if chunk.Sequence > next {
		return next, ErrSequenceGap
	}
	if chunk.Sequence < next {
		stored, found, err := s.store.Find(ctx, chunk)
		if err != nil {
			return next, err
		}
		if !found || stored.Content != chunk.Content {
			return next, ErrChunkConflict
		}
		return next, nil
	}
	if err := s.store.Insert(ctx, chunk); err != nil {
		stored, found, findErr := s.store.Find(ctx, chunk)
		if findErr == nil && found && stored.Content == chunk.Content {
			return next + 1, nil
		}
		return next, err
	}
	return next + 1, nil
}

func validChunk(chunk LogChunk) bool {
	if strings.TrimSpace(chunk.RunID) == "" || strings.TrimSpace(chunk.ExecutionToken) == "" || chunk.Sequence == 0 {
		return false
	}
	if chunk.Stream != StreamStdout && chunk.Stream != StreamStderr && chunk.Stream != StreamSystem {
		return false
	}
	return len(chunk.Content) <= DefaultChunkSize
}
