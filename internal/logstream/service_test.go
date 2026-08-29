package logstream

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"yunling.local/platform/internal/secret"
)

func TestAcceptAcknowledgesNextMissingSequence(t *testing.T) {
	store := newMemoryChunkStore()
	service := NewService(store)
	chunk := LogChunk{
		RunID: "run-1", ExecutionToken: "token-1", Sequence: 1,
		Stream: StreamStdout, Content: "第一行\n", CreatedAt: time.Now(),
	}

	next, err := service.Accept(context.Background(), chunk)
	if err != nil || next != 2 {
		t.Fatalf("首个日志块应确认下一个序号 2：next=%d err=%v", next, err)
	}
	next, err = service.Accept(context.Background(), chunk)
	if err != nil || next != 2 {
		t.Fatalf("重复日志块必须幂等确认：next=%d err=%v", next, err)
	}
	if got := store.fullText(); got != "第一行\n" {
		t.Fatalf("重复日志块不得重复保存，实际 %q", got)
	}
}

func TestAcceptRejectsGapAndConflictingDuplicate(t *testing.T) {
	store := newMemoryChunkStore()
	service := NewService(store)
	base := LogChunk{RunID: "run-1", ExecutionToken: "token-1", Sequence: 1, Stream: StreamStdout, Content: "第一行\n", CreatedAt: time.Now()}
	if _, err := service.Accept(context.Background(), base); err != nil {
		t.Fatal(err)
	}

	gap := base
	gap.Sequence = 3
	if _, err := service.Accept(context.Background(), gap); !errors.Is(err, ErrSequenceGap) {
		t.Fatalf("跳号日志必须拒绝，实际 %v", err)
	}
	conflict := base
	conflict.Content = "被篡改\n"
	if _, err := service.Accept(context.Background(), conflict); !errors.Is(err, ErrChunkConflict) {
		t.Fatalf("同序号不同内容必须拒绝，实际 %v", err)
	}
}

func TestAcceptRejectsOversizedChunk(t *testing.T) {
	service := NewService(newMemoryChunkStore())
	chunk := LogChunk{RunID: "run-1", ExecutionToken: "token-1", Sequence: 1, Stream: StreamStdout, Content: string(make([]byte, DefaultChunkSize+1))}
	if _, err := service.Accept(context.Background(), chunk); !errors.Is(err, ErrInvalidChunk) {
		t.Fatalf("超过 64 KiB 的日志块必须拒绝，实际 %v", err)
	}
}

func TestAcceptRedactsRunSecretsBeforePersistence(t *testing.T) {
	store := newMemoryChunkStore()
	value := []byte("very-secret")
	service := NewService(store, WithRedaction(secret.NewRedactor(), staticSensitiveValues{values: [][]byte{value}}))
	chunk := LogChunk{
		RunID: "run-1", ExecutionToken: "token-1", Sequence: 1, Stream: StreamStdout,
		Content: "pwd=very-secret encoded=" + base64.StdEncoding.EncodeToString(value),
	}

	if _, err := service.Accept(context.Background(), chunk); err != nil {
		t.Fatal(err)
	}
	if got := store.fullText(); got != "pwd=****** encoded=******" {
		t.Fatalf("日志必须在持久化前完成脱敏：%q", got)
	}
}

type staticSensitiveValues struct{ values [][]byte }

func (s staticSensitiveValues) ValuesForRun(context.Context, string, string) ([][]byte, error) {
	return s.values, nil
}

type memoryChunkStore struct {
	items map[string]LogChunk
}

func newMemoryChunkStore() *memoryChunkStore { return &memoryChunkStore{items: map[string]LogChunk{}} }

func (s *memoryChunkStore) NextSequence(_ context.Context, runID, token string, stream Stream) (uint64, error) {
	sequence := uint64(1)
	for {
		if _, ok := s.items[chunkKey(runID, token, stream, sequence)]; !ok {
			return sequence, nil
		}
		sequence++
	}
}

func (s *memoryChunkStore) Find(_ context.Context, chunk LogChunk) (LogChunk, bool, error) {
	stored, ok := s.items[chunkKey(chunk.RunID, chunk.ExecutionToken, chunk.Stream, chunk.Sequence)]
	return stored, ok, nil
}

func (s *memoryChunkStore) Insert(_ context.Context, chunk LogChunk) error {
	key := chunkKey(chunk.RunID, chunk.ExecutionToken, chunk.Stream, chunk.Sequence)
	if _, exists := s.items[key]; exists {
		return errors.New("重复")
	}
	s.items[key] = chunk
	return nil
}

func (s *memoryChunkStore) fullText() string {
	var result string
	for sequence := uint64(1); ; sequence++ {
		chunk, ok := s.items[chunkKey("run-1", "token-1", StreamStdout, sequence)]
		if !ok {
			return result
		}
		result += chunk.Content
	}
}

func chunkKey(runID, token string, stream Stream, sequence uint64) string {
	return runID + "/" + token + "/" + string(stream) + "/" + string(rune(sequence))
}
