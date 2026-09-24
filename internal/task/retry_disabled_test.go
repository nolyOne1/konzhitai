package task_test

import (
	"context"
	"errors"
	"testing"

	"yunling.local/platform/internal/task"
	"yunling.local/platform/internal/testpostgres"
)

func TestManualRetryCannotCreateRunForDisabledDefinition(t *testing.T) {
	db := taskDatabase(t)
	testpostgres.ApplyMigration(t, db, "000007_run_observability.up.sql")
	ctx := context.Background()
	userID := insertTaskUser(t, db)
	scriptID := insertTaskScript(t, db, userID)
	_ = insertTaskVersion(t, db, scriptID, userID, 1)
	service := task.NewService(db, taskClock)
	input := validTaskInput(scriptID, userID, "停用禁止重试")
	input.Idempotent = true
	input.RetryPolicy.MaxRetries = 1
	definition, err := service.Create(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	run, err := service.Trigger(ctx, definition.ID, task.Trigger{Type: task.TriggerManual})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE task_runs SET state='failed', process_confirmed_gone=true WHERE id=$1`, run.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.SetEnabled(ctx, definition.ID, false, false); err != nil {
		t.Fatal(err)
	}
	store := task.NewPostgresReconcileStore(db)
	if _, err := store.RetryRun(ctx, task.RunID(run.ID), taskClock()); !errors.Is(err, task.ErrRunNotRetryable) {
		t.Fatalf("停用定义不得由人工重试创建实例：%v", err)
	}
	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM task_runs WHERE task_definition_id=$1`, definition.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("不得新增运行：count=%d err=%v", count, err)
	}
	if err := service.SetEnabled(ctx, definition.ID, true, false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RetryRun(ctx, task.RunID(run.ID), taskClock()); err != nil {
		t.Fatalf("重新启用后可安全重试：%v", err)
	}
}
