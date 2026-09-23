package scheduler_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"yunling.local/platform/internal/scheduler"
	"yunling.local/platform/internal/task"
)

func TestArtifactRunWaitsForCapableAgentWithVisibleReason(t *testing.T) {
	db := schedulerDatabase(t)
	ctx := context.Background()
	now := time.Now().UTC()
	ids := seedSchedulingFixture(t, db, now, `{"artifacts":{"allowedGlobs":["*.csv"],"maxBytes":1024}}`)
	store := scheduler.NewPostgresStore(db)
	run, err := store.Get(ctx, ids.runID)
	if err != nil || !run.RequiresArtifacts {
		t.Fatalf("必须从锁定版本读取产物策略：%+v %v", run, err)
	}
	svc := scheduler.NewService(store, store, newMemoryLeases(task.Resources{}), func() time.Time { return now })
	for i := 0; i < 2; i++ {
		outcome, err := svc.ScheduleOne(ctx, run.ID)
		if err != nil || outcome != scheduler.OutcomeQueued {
			t.Fatalf("旧代理应继续排队：%s %v", outcome, err)
		}
	}
	var reason, state string
	var events int
	if err := db.QueryRow(ctx, `SELECT state,result_summary FROM task_runs WHERE id=$1`, run.ID).Scan(&state, &reason); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM run_events WHERE task_run_id=$1 AND event_type='run.queue_reason'`, run.ID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if state != "queued" || !strings.Contains(reason, "run_artifacts_v1") || events != 1 {
		t.Fatalf("排队原因应可见且不重复追加：%s %s events=%d", state, reason, events)
	}
	// The focused scheduler fixture predates the agent-management migration.
	if _, err := db.Exec(ctx, `ALTER TABLE servers ADD COLUMN agent_capabilities jsonb NOT NULL DEFAULT '[]'::jsonb`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE servers SET agent_capabilities='["run_artifacts_v1"]'::jsonb WHERE id=$1`, ids.serverID); err != nil {
		t.Fatal(err)
	}
	snapshots, err := store.Snapshots(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	if candidates := scheduler.Filter(run, snapshots); len(candidates) != 1 {
		t.Fatalf("能力上报后应恢复为候选：%+v", candidates)
	}
}
