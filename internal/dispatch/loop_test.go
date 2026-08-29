package dispatch_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"yunling.local/platform/internal/dispatch"
)

func TestRunLoopDispatchesImmediatelyContinuesAfterErrorAndStops(t *testing.T) {
	dispatcher := &loopDispatcher{calls: make(chan int, 4), firstError: errors.New("首次扫描失败")}
	logged := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		dispatch.RunLoop(ctx, dispatcher, 5*time.Millisecond, func(err error) { logged <- err })
		close(done)
	}()

	if call := receiveLoopCall(t, dispatcher.calls); call != 1 {
		t.Fatalf("启动时必须立即扫描：call=%d", call)
	}
	select {
	case err := <-logged:
		if err == nil || err.Error() != "首次扫描失败" {
			t.Fatalf("首次错误未交给日志回调：%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("首次扫描错误未在 1 秒内记录")
	}
	if call := receiveLoopCall(t, dispatcher.calls); call < 2 {
		t.Fatalf("一次错误后必须继续扫描：call=%d", call)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("取消上下文后派发循环未在 1 秒内退出")
	}
	count := dispatcher.count()
	time.Sleep(15 * time.Millisecond)
	if dispatcher.count() != count {
		t.Fatalf("派发循环退出后仍在扫描：before=%d after=%d", count, dispatcher.count())
	}
}

func receiveLoopCall(t *testing.T, calls <-chan int) int {
	t.Helper()
	select {
	case call := <-calls:
		return call
	case <-time.After(time.Second):
		t.Fatal("1 秒内未收到派发扫描")
		return 0
	}
}

type loopDispatcher struct {
	mu         sync.Mutex
	callCount  int
	calls      chan int
	firstError error
}

func (d *loopDispatcher) Dispatch(context.Context) error {
	d.mu.Lock()
	d.callCount++
	call := d.callCount
	d.mu.Unlock()
	d.calls <- call
	if call == 1 {
		return d.firstError
	}
	return nil
}

func (d *loopDispatcher) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.callCount
}
