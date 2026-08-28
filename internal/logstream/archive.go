package logstream

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"yunling.local/platform/internal/artifact"
	"yunling.local/platform/internal/task"
)

var (
	ErrRunNotArchivable = errors.New("运行日志尚不满足归档条件")
	ErrArtifactRejected = errors.New("运行产物不符合任务允许范围")
)

type ArchiveRecord struct {
	RunID      task.RunID
	ObjectKey  string
	ByteSize   int64
	SHA256     string
	FirstLogAt time.Time
	LastLogAt  time.Time
	ArchivedAt time.Time
}

type archiveLogEntry struct {
	RunID     string    `json:"runId"`
	Sequence  uint64    `json:"sequence"`
	Stream    Stream    `json:"stream"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"createdAt"`
}

type ArchiveRepository interface {
	LoadForArchive(ctx context.Context, runID task.RunID) (chunks []LogChunk, completed bool, err error)
	SaveArchive(ctx context.Context, record ArchiveRecord) error
}

type Archiver struct {
	repository ArchiveRepository
	objects    artifact.Store
	threshold  int64
	now        func() time.Time
}

func NewArchiver(repository ArchiveRepository, objects artifact.Store, threshold int64, now func() time.Time) *Archiver {
	if now == nil {
		now = time.Now
	}
	return &Archiver{repository: repository, objects: objects, threshold: threshold, now: now}
}

func (a *Archiver) Archive(ctx context.Context, runID task.RunID) (string, error) {
	if a == nil || a.repository == nil || a.objects == nil || strings.TrimSpace(string(runID)) == "" || a.threshold < 0 {
		return "", ErrRunNotArchivable
	}
	chunks, completed, err := a.repository.LoadForArchive(ctx, runID)
	if err != nil {
		return "", err
	}
	var uncompressed int64
	for _, chunk := range chunks {
		uncompressed += int64(len(chunk.Content))
	}
	if !completed || len(chunks) == 0 || uncompressed < a.threshold {
		return "", ErrRunNotArchivable
	}
	var compressed bytes.Buffer
	writer, err := gzip.NewWriterLevel(&compressed, gzip.BestCompression)
	if err != nil {
		return "", err
	}
	writer.Header.ModTime = time.Unix(0, 0).UTC()
	encoder := json.NewEncoder(writer)
	for _, chunk := range chunks {
		if err := encoder.Encode(archiveLogEntry{
			RunID: chunk.RunID, Sequence: chunk.Sequence, Stream: chunk.Stream,
			Content: chunk.Content, CreatedAt: chunk.CreatedAt,
		}); err != nil {
			_ = writer.Close()
			return "", fmt.Errorf("编码归档日志：%w", err)
		}
	}
	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("压缩归档日志：%w", err)
	}
	sum := sha256.Sum256(compressed.Bytes())
	checksum := hex.EncodeToString(sum[:])
	key := "runs/" + string(runID) + "/logs.ndjson.gz"
	if err := a.objects.Put(ctx, key, bytes.NewReader(compressed.Bytes()), int64(compressed.Len()), checksum); err != nil {
		return "", fmt.Errorf("保存归档日志：%w", err)
	}
	record := ArchiveRecord{
		RunID: runID, ObjectKey: key, ByteSize: int64(compressed.Len()), SHA256: checksum,
		FirstLogAt: chunks[0].CreatedAt, LastLogAt: chunks[len(chunks)-1].CreatedAt,
		ArchivedAt: a.now().UTC(),
	}
	if err := a.repository.SaveArchive(ctx, record); err != nil {
		return "", err
	}
	return key, nil
}

type ArtifactPolicy struct {
	AllowedGlobs []string
	MaxBytes     int64
}

type ArtifactRecord struct {
	RunID     task.RunID
	Name      string
	ObjectKey string
	ByteSize  int64
	SHA256    string
	CreatedAt time.Time
}

type ArtifactRepository interface {
	SaveArtifact(context.Context, ArtifactRecord) error
}

type ArtifactIngestor struct {
	repository ArtifactRepository
	objects    artifact.Store
	now        func() time.Time
}

func NewArtifactIngestor(repository ArtifactRepository, objects artifact.Store) *ArtifactIngestor {
	return &ArtifactIngestor{repository: repository, objects: objects, now: time.Now}
}

func (i *ArtifactIngestor) Store(ctx context.Context, runID task.RunID, name string, policy ArtifactPolicy, body io.Reader, size int64, checksum string) (string, error) {
	name = strings.TrimSpace(name)
	if i == nil || i.repository == nil || i.objects == nil || body == nil || strings.TrimSpace(string(runID)) == "" ||
		name == "" || path.Base(name) != name || strings.Contains(name, "\\") || size < 0 || policy.MaxBytes < 0 || size > policy.MaxBytes ||
		!matchesAny(name, policy.AllowedGlobs) || len(checksum) != 64 {
		return "", ErrArtifactRejected
	}
	contents, err := io.ReadAll(io.LimitReader(body, policy.MaxBytes+1))
	if err != nil || int64(len(contents)) != size || int64(len(contents)) > policy.MaxBytes {
		return "", ErrArtifactRejected
	}
	sum := sha256.Sum256(contents)
	actual := hex.EncodeToString(sum[:])
	if !strings.EqualFold(actual, checksum) {
		return "", ErrArtifactRejected
	}
	key := "runs/" + string(runID) + "/artifacts/" + name
	if err := i.objects.Put(ctx, key, bytes.NewReader(contents), size, actual); err != nil {
		return "", fmt.Errorf("保存运行产物：%w", err)
	}
	if err := i.repository.SaveArtifact(ctx, ArtifactRecord{
		RunID: runID, Name: name, ObjectKey: key, ByteSize: size,
		SHA256: actual, CreatedAt: i.now().UTC(),
	}); err != nil {
		return "", err
	}
	return key, nil
}

func matchesAny(name string, patterns []string) bool {
	for _, pattern := range patterns {
		if matched, err := path.Match(pattern, name); err == nil && matched {
			return true
		}
	}
	return false
}

type PostgresArchiveRepository struct{ db *pgxpool.Pool }

func NewPostgresArchiveRepository(db *pgxpool.Pool) *PostgresArchiveRepository {
	return &PostgresArchiveRepository{db: db}
}

func (r *PostgresArchiveRepository) LoadForArchive(ctx context.Context, runID task.RunID) ([]LogChunk, bool, error) {
	var state task.RunState
	if err := r.db.QueryRow(ctx, `SELECT state FROM task_runs WHERE id=$1`, runID).Scan(&state); errors.Is(err, pgx.ErrNoRows) {
		return nil, false, task.ErrRunNotFound
	} else if err != nil {
		return nil, false, fmt.Errorf("读取归档任务状态：%w", err)
	}
	rows, err := r.db.Query(ctx, `
		SELECT task_run_id::text, execution_token, sequence, stream, content, created_at
		FROM log_chunks WHERE task_run_id=$1 ORDER BY created_at, stream, sequence
	`, runID)
	if err != nil {
		return nil, false, fmt.Errorf("读取待归档日志：%w", err)
	}
	defer rows.Close()
	chunks := []LogChunk{}
	for rows.Next() {
		var chunk LogChunk
		if err := rows.Scan(&chunk.RunID, &chunk.ExecutionToken, &chunk.Sequence, &chunk.Stream, &chunk.Content, &chunk.CreatedAt); err != nil {
			return nil, false, err
		}
		chunks = append(chunks, chunk)
	}
	return chunks, state.Terminal(), rows.Err()
}

func (r *PostgresArchiveRepository) SaveArchive(ctx context.Context, record ArchiveRecord) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO run_log_archives (
			task_run_id, object_key, byte_size, sha256, first_log_at, last_log_at, archived_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (task_run_id) DO NOTHING
	`, record.RunID, record.ObjectKey, record.ByteSize, record.SHA256,
		record.FirstLogAt, record.LastLogAt, record.ArchivedAt)
	if err != nil {
		return fmt.Errorf("保存日志归档索引：%w", err)
	}
	return nil
}

func (r *PostgresArchiveRepository) SaveArtifact(ctx context.Context, record ArtifactRecord) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO run_artifacts (task_run_id, name, object_key, byte_size, sha256, created_at)
		VALUES ($1,$2,$3,$4,$5,$6)
	`, record.RunID, record.Name, record.ObjectKey, record.ByteSize, record.SHA256, record.CreatedAt)
	if err != nil {
		return fmt.Errorf("保存运行产物索引：%w", err)
	}
	return nil
}

var (
	_ ArchiveRepository  = (*PostgresArchiveRepository)(nil)
	_ ArtifactRepository = (*PostgresArchiveRepository)(nil)
)
