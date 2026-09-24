package task_test

import (
	"context"
	"testing"
	"time"

	"yunling.local/platform/internal/task"
	"yunling.local/platform/internal/testpostgres"
)

func TestRunHistorySearchIncludesOldRecordsAndPagesInDatabase(t *testing.T) {
	db := taskDatabase(t)
	testpostgres.ApplyMigration(t, db, "000007_run_observability.up.sql")
	// This focused fixture intentionally applies only the task tables.
	if _, err := db.Exec(context.Background(), `CREATE TABLE schema_migrations (version bigint PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	testpostgres.ApplyMigration(t, db, "000018_unlimited_task_wait.up.sql")
	ctx := context.Background()
	userID := insertTaskUser(t, db)
	scriptID := insertTaskScript(t, db, userID)
	_ = insertTaskVersion(t, db, scriptID, userID, 1)
	service := task.NewService(db, taskClock)
	input := validTaskInput(scriptID, userID, "历史订单")
	input.MaxWaitSeconds = 0
	definition, err := service.Create(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	original, err := service.Trigger(ctx, definition.ID, task.Trigger{Type: task.TriggerManual})
	if err != nil || original.MaxWaitSeconds != 0 {
		t.Fatalf("无限等待快照未保存：%+v %v", original, err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO task_runs (task_definition_id,script_version_id,trigger_type,state,created_at,queued_at,required_labels,required_runtime)
        SELECT $1,$2,'manual','succeeded',$3::timestamptz + n * interval '1 minute',$3::timestamptz + n * interval '1 minute','{}'::jsonb,'bash'
        FROM generate_series(1,205) n`, definition.ID, original.ScriptVersionID, taskClock()); err != nil {
		t.Fatal(err)
	}
	runs := task.NewRunService(db, nil, nil, taskClock)
	until := taskClock().Add(time.Second)
	page, err := runs.QueryRuns(ctx, task.RunFilter{Query: "历史订单", State: task.Queued, DefinitionID: definition.ID, ScriptID: scriptID, Until: &until, Limit: 50})
	if err != nil || len(page.Runs) != 1 || page.Runs[0].ID != original.ID || page.HasMore {
		t.Fatalf("200条之前的记录应可按条件查到：%+v %v", page, err)
	}
	first, err := runs.QueryRuns(ctx, task.RunFilter{Limit: 50})
	if err != nil || len(first.Runs) != 50 || !first.HasMore {
		t.Fatalf("第一页应保留分页信息：%+v %v", first, err)
	}
	last, err := runs.QueryRuns(ctx, task.RunFilter{Limit: 50, Offset: 200})
	if err != nil || len(last.Runs) != 6 || last.HasMore || last.Runs[5].ID != original.ID {
		t.Fatalf("历史末页不正确：%+v %v", last, err)
	}
}
