package logstream

import (
	"context"
	"errors"
	"fmt"
	"time"

	"yunling.local/platform/internal/task"
)

const (
	DefaultArchiveThreshold int64 = 1 << 20
	ArchiveQuietPeriod            = 30 * time.Second
)

type ArchiveCandidateSource interface {
	ListArchiveCandidates(context.Context, int64, time.Time, int) ([]task.RunID, error)
}

type ArchiveWorker struct {
	candidates ArchiveCandidateSource
	archiver   *Archiver
	now        func() time.Time
}

func NewArchiveWorker(candidates ArchiveCandidateSource, archiver *Archiver, now func() time.Time) *ArchiveWorker {
	if now == nil {
		now = time.Now
	}
	return &ArchiveWorker{candidates: candidates, archiver: archiver, now: now}
}

func (w *ArchiveWorker) Sweep(ctx context.Context) error {
	ids, err := w.candidates.ListArchiveCandidates(ctx, w.archiver.threshold, w.now().Add(-ArchiveQuietPeriod), 10)
	if err != nil {
		return err
	}
	var failures []error
	for _, id := range ids {
		if ctx.Err() != nil {
			return errors.Join(append(failures, ctx.Err())...)
		}
		if _, err := w.archiver.Archive(ctx, id); err != nil && !errors.Is(err, ErrRunNotArchivable) {
			failures = append(failures, fmt.Errorf("归档运行 %s：%w", id, err))
		}
	}
	return errors.Join(failures...)
}

func RunArchiveLoop(ctx context.Context, worker *ArchiveWorker, interval time.Duration, onError func(error)) {
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		attemptCtx, cancel := context.WithTimeout(ctx, time.Minute)
		err := worker.Sweep(attemptCtx)
		cancel()
		if err != nil && onError != nil {
			onError(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r *PostgresArchiveRepository) ListArchiveCandidates(ctx context.Context, threshold int64, quietBefore time.Time, limit int) ([]task.RunID, error) {
	rows, err := r.db.Query(ctx, `
		SELECT run.id
		FROM task_runs AS run
		JOIN LATERAL (
			SELECT COALESCE(MAX(archive_cursor),0) AS cursor, COUNT(*) AS chunks,
				COALESCE(SUM(byte_size),0) AS bytes, MAX(received_at) AS last_received_at
			FROM log_chunks WHERE task_run_id=run.id
		) AS logs ON logs.chunks > 0
		LEFT JOIN run_log_archives AS archive ON archive.task_run_id=run.id
		WHERE run.state IN ('succeeded','failed','timed_out','cancelled','expired')
			AND logs.bytes >= $1 AND logs.last_received_at <= $2
			AND COALESCE(run.finished_at, run.updated_at) <= $2
			AND (archive.task_run_id IS NULL OR archive.last_log_cursor <> logs.cursor OR archive.chunk_count <> logs.chunks)
		ORDER BY COALESCE(archive.archived_at, run.finished_at, run.created_at), run.id
		LIMIT $3
	`, threshold, quietBefore, limit)
	if err != nil {
		return nil, fmt.Errorf("读取日志归档候选：%w", err)
	}
	defer rows.Close()
	ids := []task.RunID{}
	for rows.Next() {
		var id task.RunID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
