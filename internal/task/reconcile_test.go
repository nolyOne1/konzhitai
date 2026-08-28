package task

import (
	"context"
	"errors"
	"testing"
	"time"

	"yunling.local/platform/internal/agentprotocol"
)

func TestOfflineRunningServerMarksRunUnknownWithoutRetry(t *testing.T) {
	store := &memoryReconcileStore{state: Running}
	reconciler := NewReconciler(store, func() time.Time { return time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC) })

	if err := reconciler.ServerOffline(context.Background(), "server-1"); err != nil {
		t.Fatal(err)
	}
	if store.state != Unknown || store.retryCount != 0 {
		t.Fatalf("服务器失联只能把运行任务标为待确认，不得自动重试：state=%s retries=%d", store.state, store.retryCount)
	}
}

func TestReconnectRestoresMatchingRunAndConfirmsMissingProcessGone(t *testing.T) {
	store := &memoryReconcileStore{state: Unknown}
	reconciler := NewReconciler(store, time.Now)
	report := agentprotocol.RunningReport{ServerID: "server-1", ReportedAt: time.Now(), Processes: []agentprotocol.RunningProcess{{RunID: "run-1", ExecutionToken: "token-1"}}}

	if err := reconciler.Reconcile(context.Background(), report); err != nil {
		t.Fatal(err)
	}
	if store.state != Running || store.report.ServerID != "server-1" {
		t.Fatalf("匹配执行令牌的任务应恢复运行：state=%s report=%+v", store.state, store.report)
	}
}

func TestRetryRequiresIdempotentPolicyAndConfirmedGone(t *testing.T) {
	store := &memoryReconcileStore{state: Unknown, retryErr: ErrRunNotRetryable}
	reconciler := NewReconciler(store, time.Now)
	if _, err := reconciler.Retry(context.Background(), "run-1"); !errors.Is(err, ErrRunNotRetryable) {
		t.Fatalf("未确认原进程结束时必须拒绝重试，实际 %v", err)
	}
	store.retryErr = nil
	store.retryID = "run-2"
	if runID, err := reconciler.Retry(context.Background(), "run-1"); err != nil || runID != "run-2" {
		t.Fatalf("满足幂等和重试策略后应创建新实例：run=%s err=%v", runID, err)
	}
}

type memoryReconcileStore struct {
	state      RunState
	report     agentprotocol.RunningReport
	retryCount int
	retryID    RunID
	retryErr   error
}

func (s *memoryReconcileStore) MarkServerRunsUnknown(_ context.Context, _ string, _ time.Time) error {
	s.state = Unknown
	return nil
}

func (s *memoryReconcileStore) ReconcileRunning(_ context.Context, report agentprotocol.RunningReport, _ time.Time) error {
	s.report = report
	if len(report.Processes) > 0 {
		s.state = Running
	}
	return nil
}

func (s *memoryReconcileStore) RetryRun(_ context.Context, _ RunID, _ time.Time) (RunID, error) {
	if s.retryErr != nil {
		return "", s.retryErr
	}
	s.retryCount++
	return s.retryID, nil
}
