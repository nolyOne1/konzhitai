package agent

import (
	"context"
	"testing"
	"time"

	"yunling.local/platform/internal/agentprotocol"
	"yunling.local/platform/internal/logstream"
)

func TestLogClientPersistsUploadsAndDeletesOnlyAcknowledgedChunks(t *testing.T) {
	spool, err := logstream.NewSpool(t.TempDir(), logstream.DefaultChunkSize)
	if err != nil {
		t.Fatal(err)
	}
	transport := &fakeLogTransport{received: make(chan agentprotocol.LogChunk, 1)}
	client := NewLogClient(spool, transport)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx) }()

	writer := client.OutputWriter("run-1", "token-1", "stdout")
	if _, err := writer.Write([]byte("第一行\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case chunk := <-transport.received:
		if chunk.RunID != "run-1" || chunk.Sequence != 1 || chunk.Content != "第一行\n" {
			t.Fatalf("上传的日志块不正确：%+v", chunk)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("日志块未及时上传")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		pending, err := spool.Pending("run-1", "token-1", logstream.StreamStdout)
		if err != nil {
			t.Fatal(err)
		}
		if len(pending) == 0 {
			cancel()
			<-done
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("中央确认后未清理本地日志块")
}

type fakeLogTransport struct{ received chan agentprotocol.LogChunk }

func (t *fakeLogTransport) SendLogChunk(_ context.Context, chunk agentprotocol.LogChunk) (uint64, error) {
	t.received <- chunk
	return chunk.Sequence + 1, nil
}
