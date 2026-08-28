package task_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"yunling.local/platform/internal/agentprotocol"
	"yunling.local/platform/internal/logstream"
	"yunling.local/platform/internal/task"
	"yunling.local/platform/internal/testpostgres"
)

func TestPostgresRuntimeEventsLogsAndReconciliationAreIdempotent(t *testing.T) {
	db := taskDatabase(t)
	testpostgres.ApplyMigration(t, db, "000007_run_observability.up.sql")
	ctx := context.Background()
	userID := insertTaskUser(t, db)
	scriptID := insertTaskScript(t, db, userID)
	_ = insertTaskVersion(t, db, scriptID, userID, 1)
	service := task.NewService(db, taskClock)
	input := validTaskInput(scriptID, userID, "运行状态联调")
	input.Idempotent = true
	input.RetryPolicy = task.RetryPolicy{MaxRetries: 1, BackoffSeconds: 0}
	definition, err := service.Create(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	run, err := service.Trigger(ctx, definition.ID, task.Trigger{Type: task.TriggerManual, RequestedBy: userID})
	if err != nil {
		t.Fatal(err)
	}
	var serverID string
	if err := db.QueryRow(ctx, `INSERT INTO servers (name,status) VALUES ('运行节点','online') RETURNING id`).Scan(&serverID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE task_runs SET state='assigned', assigned_server_id=$2, execution_token='token-1' WHERE id=$1`, run.ID, serverID); err != nil {
		t.Fatal(err)
	}

	events := task.NewEventService(task.NewPostgresRunEventStore(db))
	started := agentprotocol.RunEvent{RunID: run.ID, ExecutionToken: "token-1", Sequence: 1, Type: "started", OccurredAt: time.Now().UTC(), Message: "任务已开始执行"}
	if err := events.Apply(ctx, started); err != nil {
		t.Fatal(err)
	}
	if err := events.Apply(ctx, started); err != nil {
		t.Fatalf("重复事件必须幂等：%v", err)
	}
	gap := started
	gap.Sequence = 3
	gap.Type = "succeeded"
	if err := events.Apply(ctx, gap); !errors.Is(err, task.ErrRunEventSequence) {
		t.Fatalf("跳号运行事件必须拒绝：%v", err)
	}
	logs := logstream.NewService(logstream.NewPostgresChunkStore(db))
	chunk := logstream.LogChunk{RunID: run.ID, ExecutionToken: "token-1", Sequence: 1, Stream: logstream.StreamStdout, Content: "执行中\n", CreatedAt: time.Now().UTC()}
	if next, err := logs.Accept(ctx, chunk); err != nil || next != 2 {
		t.Fatalf("保存日志失败：next=%d err=%v", next, err)
	}
	if next, err := logs.Accept(ctx, chunk); err != nil || next != 2 {
		t.Fatalf("重复日志必须幂等：next=%d err=%v", next, err)
	}

	reconciler := task.NewReconciler(task.NewPostgresReconcileStore(db), time.Now)
	if err := reconciler.ServerOffline(ctx, serverID); err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Reconcile(ctx, agentprotocol.RunningReport{ServerID: serverID, ReportedAt: time.Now(), Processes: []agentprotocol.RunningProcess{}}); err != nil {
		t.Fatal(err)
	}
	var processConfirmedGone bool
	if err := db.QueryRow(ctx, `SELECT process_confirmed_gone FROM task_runs WHERE id=$1`, run.ID).Scan(&processConfirmedGone); err != nil {
		t.Fatal(err)
	}
	if processConfirmedGone {
		t.Fatal("非权威进程清单不能证明原执行进程已经结束")
	}
	if _, err := reconciler.Retry(ctx, run.ID); err != task.ErrRunNotRetryable {
		t.Fatalf("未确认原进程结束前不得重试：%v", err)
	}
	if err := reconciler.Reconcile(ctx, agentprotocol.RunningReport{ServerID: serverID, ReportedAt: time.Now(), Authoritative: true, Processes: []agentprotocol.RunningProcess{}}); err != nil {
		t.Fatal(err)
	}
	detail, err := task.NewRunService(db, nil, reconciler, time.Now).GetRun(ctx, run.ID)
	if err != nil || !detail.ProcessConfirmedGone {
		t.Fatalf("执行详情必须公开安全重试判定：detail=%+v err=%v", detail, err)
	}
	retryID, err := reconciler.Retry(ctx, run.ID)
	if err != nil || retryID == "" {
		t.Fatalf("确认进程结束后幂等任务应可重试：run=%s err=%v", retryID, err)
	}
	var eventCount, logCount int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM run_events WHERE task_run_id=$1 AND agent_sequence=1`, run.ID).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM log_chunks WHERE task_run_id=$1`, run.ID).Scan(&logCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 1 || logCount != 1 {
		t.Fatalf("事件和日志重复上报不得重复保存：events=%d logs=%d", eventCount, logCount)
	}
}
