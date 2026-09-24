package task_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"yunling.local/platform/internal/agentprotocol"
	"yunling.local/platform/internal/task"
	"yunling.local/platform/internal/testpostgres"
)

func TestRunUsageIsBoundToExecutionAndExposedAfterTerminalReplay(t *testing.T) {
	db := taskDatabase(t)
	testpostgres.ApplyMigration(t, db, "000007_run_observability.up.sql")
	ctx := context.Background()
	userID := insertTaskUser(t, db)
	scriptID := insertTaskScript(t, db, userID)
	_ = insertTaskVersion(t, db, scriptID, userID, 1)
	service := task.NewService(db, taskClock)
	definition, err := service.Create(ctx, validTaskInput(scriptID, userID, "资源用量"))
	if err != nil {
		t.Fatal(err)
	}
	run, err := service.Trigger(ctx, definition.ID, task.Trigger{Type: task.TriggerManual})
	if err != nil {
		t.Fatal(err)
	}
	var serverID string
	if err := db.QueryRow(ctx, `INSERT INTO servers (name,status) VALUES ('资源采样节点','online') RETURNING id`).Scan(&serverID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE task_runs SET state='running',assigned_server_id=$2,execution_token='usage-token' WHERE id=$1`, run.ID, serverID); err != nil {
		t.Fatal(err)
	}
	now := taskClock().Add(time.Hour)
	counter := func(value int64) *int64 { return &value }
	sample := &agentprotocol.ResourceUsage{CPUTimeMillis: counter(100), MemoryBytes: counter(2048), SampledAt: now}
	report := agentprotocol.RunningReport{ServerID: serverID, ReportedAt: now, Processes: []agentprotocol.RunningProcess{{RunID: run.ID, ExecutionToken: "stale-token", Usage: sample}}}
	store := task.NewPostgresReconcileStore(db)
	countUsage := func(want int) {
		t.Helper()
		var count int
		if err := db.QueryRow(ctx, `SELECT count(*) FROM run_events WHERE task_run_id=$1 AND payload->'usage' IS NOT NULL`, run.ID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != want {
			t.Fatalf("资源样本数量：got=%d want=%d", count, want)
		}
	}
	if err := store.ReconcileRunning(ctx, report, now); err != nil {
		t.Fatal(err)
	}
	countUsage(0)
	report.ServerID = "11111111-1111-4111-8111-111111111111"
	report.Processes[0].ExecutionToken = "usage-token"
	if err := store.ReconcileRunning(ctx, report, now); err != nil {
		t.Fatal(err)
	}
	countUsage(0)
	report.ServerID = serverID
	for i := 0; i < 2; i++ {
		if err := store.ReconcileRunning(ctx, report, now); err != nil {
			t.Fatal(err)
		}
	}
	countUsage(1)
	newest := &agentprotocol.ResourceUsage{CPUTimeMillis: counter(150), PeakMemoryBytes: counter(4096), SampledAt: now.Add(time.Second)}
	report.Processes[0].Usage = newest
	if err := store.ReconcileRunning(ctx, report, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	report.Processes[0].Usage = sample
	if err := store.ReconcileRunning(ctx, report, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	countUsage(2)
	runs := task.NewRunService(db, nil, nil, taskClock)
	detail, err := runs.GetRun(ctx, run.ID)
	if err != nil || !newest.Equal(detail.Usage) {
		t.Fatalf("详情应返回最新样本：%+v %v", detail.Usage, err)
	}

	finalSample := &agentprotocol.ResourceUsage{CPUTimeMillis: counter(200), PeakMemoryBytes: counter(8192), Processes: counter(0), SampledAt: now.Add(3 * time.Second)}
	event := agentprotocol.RunEvent{RunID: run.ID, ExecutionToken: "usage-token", Sequence: 1, Type: "succeeded", Usage: finalSample, OccurredAt: now.Add(3 * time.Second)}
	events := task.NewEventService(task.NewPostgresRunEventStore(db))
	for i := 0; i < 2; i++ {
		if err := events.Apply(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	countUsage(3)
	conflicting := event
	changed := *finalSample
	changed.CPUTimeMillis = counter(201)
	conflicting.Usage = &changed
	if err := events.Apply(ctx, conflicting); !errors.Is(err, task.ErrRunEventConflict) {
		t.Fatalf("终态重放改变资源数据必须冲突：%v", err)
	}
	stale := event
	stale.ExecutionToken = "stale-token"
	if err := events.Apply(ctx, stale); !errors.Is(err, task.ErrExecutionTokenMismatch) {
		t.Fatalf("旧令牌不得重写终态采样：%v", err)
	}
	detail, err = runs.GetRun(ctx, run.ID)
	if err != nil || detail.State != task.Succeeded || !finalSample.Equal(detail.Usage) {
		t.Fatalf("最终用量不可丢失：%+v %v", detail, err)
	}
	stream, err := runs.ListRunEvents(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range stream {
		if item.EventType == "run.succeeded" && finalSample.Equal(item.Usage) {
			found = true
		}
	}
	if !found {
		t.Fatal("SSE事件必须携带终态资源采样")
	}
}
