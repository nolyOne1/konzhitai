package agentupgrade

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type loopScanner struct{ calls atomic.Int32 }

func (s *loopScanner) Scan(context.Context) error { s.calls.Add(1); return nil }
func TestRunLoopStopsWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	scanner := &loopScanner{}
	done := make(chan struct{})
	go func() { RunLoop(ctx, scanner, time.Millisecond, nil); close(done) }()
	for scanner.calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("编排循环未停止")
	}
}
