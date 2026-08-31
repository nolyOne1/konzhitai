package ops

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestLoopContinuesOutboxWhenRulesFailAndStopsOnCancel(t *testing.T) {
	rules := &countingRuleScanner{err: errors.New("rule failed")}
	outbox := &countingOutboxScanner{}
	health := &countingSuccessRecorder{}
	loop := NewLoop(rules, outbox, 10*time.Millisecond, time.Second, health, func(error) {})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- loop.Run(ctx) }()

	deadline := time.After(time.Second)
	for outbox.calls.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("规则失败不得阻止发件箱扫描")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("取消 context 后循环必须在 1 秒内退出")
	}
	if rules.calls.Load() == 0 || health.calls.Load() == 0 {
		t.Fatalf("扫描与健康记录未执行：rules=%d health=%d", rules.calls.Load(), health.calls.Load())
	}
}

type recordingOperationalScanner struct{ calls []string }

func (s *recordingOperationalScanner) EnsureSchedules(_ context.Context, _ time.Time) error {
	s.calls = append(s.calls, "schedule")
	return nil
}
func (s *recordingOperationalScanner) RunBackup(context.Context) error {
	s.calls = append(s.calls, "backup")
	return nil
}
func (s *recordingOperationalScanner) RunVerification(context.Context) error {
	s.calls = append(s.calls, "verification")
	return nil
}

func TestLoopSchedulesThenRunsBackupAndVerification(t *testing.T) {
	operational := &recordingOperationalScanner{}
	loop := NewLoop(&countingRuleScanner{}, &countingOutboxScanner{}, time.Second, time.Second, nil, func(error) {}).WithBackup(operational)
	loop.now = func() time.Time { return time.Date(2026, 9, 1, 3, 30, 0, 0, time.UTC) }
	loop.runOnce(context.Background())
	if got := strings.Join(operational.calls, ","); got != "schedule,backup,verification" {
		t.Fatalf("备份调度执行顺序错误：%q", got)
	}
}

type countingRuleScanner struct {
	calls atomic.Int32
	err   error
}

func (s *countingRuleScanner) Scan(context.Context) error {
	s.calls.Add(1)
	return s.err
}

type countingOutboxScanner struct{ calls atomic.Int32 }

func (s *countingOutboxScanner) DeliverDue(context.Context) error {
	s.calls.Add(1)
	return nil
}

type countingSuccessRecorder struct{ calls atomic.Int32 }

func (r *countingSuccessRecorder) MarkSuccessfulScan(time.Time) { r.calls.Add(1) }
