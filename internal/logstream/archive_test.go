package logstream

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"
	"testing"
	"time"

	"yunling.local/platform/internal/task"
)

func TestArchiverWritesCompletedLargeLogAsNDJSONGzip(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	repository := &memoryArchiveRepository{completed: true, chunks: []LogChunk{
		{RunID: "run-1", ExecutionToken: "token-1", Sequence: 1, Stream: StreamStdout, Content: "开始\n", CreatedAt: now.Add(-time.Minute)},
		{RunID: "run-1", ExecutionToken: "token-1", Sequence: 2, Stream: StreamStdout, Content: "完成\n", CreatedAt: now},
	}}
	objects := &memoryObjectStore{items: map[string][]byte{}}
	archiver := NewArchiver(repository, objects, 1, func() time.Time { return now })

	key, err := archiver.Archive(context.Background(), task.RunID("run-1"))
	if err != nil {
		t.Fatal(err)
	}
	if key != "runs/run-1/logs.ndjson.gz" || repository.record.ObjectKey != key {
		t.Fatalf("归档对象键不正确：key=%s record=%+v", key, repository.record)
	}
	reader, err := gzip.NewReader(bytes.NewReader(objects.items[key]))
	if err != nil {
		t.Fatal(err)
	}
	plain, _ := io.ReadAll(reader)
	if !strings.Contains(string(plain), `"content":"开始\n"`) || !strings.Contains(string(plain), `"content":"完成\n"`) {
		t.Fatalf("归档必须包含逐块 NDJSON：%s", plain)
	}
	if strings.Contains(string(plain), "token-1") {
		t.Fatalf("日志归档不得包含执行令牌：%s", plain)
	}
}

func TestArtifactIngestorEnforcesGlobAndSize(t *testing.T) {
	objects := &memoryObjectStore{items: map[string][]byte{}}
	repository := &memoryArtifactRepository{}
	ingestor := NewArtifactIngestor(repository, objects)
	body := []byte("报表")
	sum := sha256.Sum256(body)
	checksum := hex.EncodeToString(sum[:])
	policy := ArtifactPolicy{AllowedGlobs: []string{"*.csv"}, MaxBytes: 64}

	if _, err := ingestor.Store(context.Background(), task.RunID("run-1"), "report.txt", policy, bytes.NewReader(body), int64(len(body)), checksum); err == nil {
		t.Fatal("不匹配允许 glob 的产物必须拒绝")
	}
	key, err := ingestor.Store(context.Background(), task.RunID("run-1"), "report.csv", policy, bytes.NewReader(body), int64(len(body)), checksum)
	if err != nil || key != "runs/run-1/artifacts/report.csv" {
		t.Fatalf("合法产物应按运行实例保存：key=%s err=%v", key, err)
	}
}

type memoryArchiveRepository struct {
	completed bool
	chunks    []LogChunk
	record    ArchiveRecord
}

func (r *memoryArchiveRepository) LoadForArchive(context.Context, task.RunID) ([]LogChunk, bool, error) {
	return r.chunks, r.completed, nil
}
func (r *memoryArchiveRepository) SaveArchive(_ context.Context, record ArchiveRecord) error {
	r.record = record
	return nil
}

type memoryObjectStore struct{ items map[string][]byte }

func (s *memoryObjectStore) Put(_ context.Context, key string, body io.Reader, _ int64, _ string) error {
	value, err := io.ReadAll(body)
	if err == nil {
		s.items[key] = value
	}
	return err
}
func (s *memoryObjectStore) Open(_ context.Context, key string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(s.items[key])), nil
}

type memoryArtifactRepository struct{ record ArtifactRecord }

func (r *memoryArtifactRepository) SaveArtifact(_ context.Context, record ArtifactRecord) error {
	r.record = record
	return nil
}
