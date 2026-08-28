package agent

import (
	"context"
	"fmt"
	"io"
	"time"

	"yunling.local/platform/internal/agentprotocol"
	"yunling.local/platform/internal/logstream"
)

type LogTransport interface {
	SendLogChunk(context.Context, agentprotocol.LogChunk) (nextSequence uint64, err error)
}

type LogClient struct {
	spool     *logstream.Spool
	transport LogTransport
	wake      chan struct{}
}

func NewLogClient(spool *logstream.Spool, transport LogTransport) *LogClient {
	return &LogClient{spool: spool, transport: transport, wake: make(chan struct{}, 1)}
}

func (c *LogClient) OutputWriter(runID, executionToken, stream string) io.Writer {
	return logOutputWriter{client: c, runID: runID, token: executionToken, stream: logstream.Stream(stream)}
}

func (c *LogClient) Run(ctx context.Context) error {
	if c == nil || c.spool == nil || c.transport == nil {
		return fmt.Errorf("日志上传客户端配置不完整")
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if err := c.flush(ctx); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-c.wake:
		case <-ticker.C:
		}
	}
}

func (c *LogClient) flush(ctx context.Context) error {
	chunks, err := c.spool.AllPending()
	if err != nil {
		return err
	}
	for _, chunk := range chunks {
		next, err := c.transport.SendLogChunk(ctx, agentprotocol.LogChunk{
			MessageType: "log_chunk", RunID: chunk.RunID, ExecutionToken: chunk.ExecutionToken,
			Sequence: chunk.Sequence, Stream: string(chunk.Stream), Content: chunk.Content,
			CreatedAt: chunk.CreatedAt,
		})
		if err != nil {
			return fmt.Errorf("上传任务日志：%w", err)
		}
		if err := c.spool.Acknowledge(chunk.RunID, chunk.ExecutionToken, chunk.Stream, next); err != nil {
			return err
		}
	}
	return nil
}

type logOutputWriter struct {
	client *LogClient
	runID  string
	token  string
	stream logstream.Stream
}

func (w logOutputWriter) Write(content []byte) (int, error) {
	if len(content) == 0 {
		return 0, nil
	}
	if _, err := w.client.spool.Append(w.runID, w.token, w.stream, content); err != nil {
		return 0, err
	}
	select {
	case w.client.wake <- struct{}{}:
	default:
	}
	return len(content), nil
}
