package task_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"yunling.local/platform/internal/task"
	"yunling.local/platform/internal/testpostgres"
)

func TestRetryRequestsReuseSuccessorAcrossConcurrencyAndAncestors(t *testing.T) {
	db := taskDatabase(t)
	testpostgres.ApplyMigration(t, db, "000007_run_observability.up.sql")
	ctx := context.Background()
	now := taskClock().Add(time.Hour)
	userID := insertTaskUser(t, db)
	scriptID := insertTaskScript(t, db, userID)
	_ = insertTaskVersion(t, db, scriptID, userID, 1)
	service := task.NewService(db, taskClock)
	input := validTaskInput(scriptID, userID, "重试去重")
	input.Idempotent = true
	input.RetryPolicy = task.RetryPolicy{MaxRetries: 2}
	definition, err := service.Create(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	run, err := service.Trigger(ctx, definition.ID, task.Trigger{Type: task.TriggerManual})
	if err != nil {
		t.Fatal(err)
	}
	fail := func(id string) {
		t.Helper()
		if _, err := db.Exec(ctx, `UPDATE task_runs SET state='failed', process_confirmed_gone=true WHERE id=$1`, id); err != nil {
			t.Fatal(err)
		}
	}
	fail(run.ID)
	store := task.NewPostgresReconcileStore(db)
	var wg sync.WaitGroup
	ids := make(chan task.RunID, 8)
	errors := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := store.RetryRun(ctx, task.RunID(run.ID), now)
			ids <- id
			errors <- err
		}()
	}
	wg.Wait()
	close(ids)
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	var successor task.RunID
	for id := range ids {
		if successor == "" {
			successor = id
		}
		if id != successor {
			t.Fatalf("requests forked: %s != %s", id, successor)
		}
	}
	fail(string(successor))
	third, err := store.RetryRun(ctx, successor, now)
	if err != nil {
		t.Fatal(err)
	}
	if third == successor {
		t.Fatal("next attempt reused its parent")
	}
	// Replaying an older request must not create a fourth instance or return
	// a different result just because its original successor has completed.
	for _, pair := range []struct{ parent, want task.RunID }{{task.RunID(run.ID), successor}, {successor, third}} {
		got, err := store.RetryRun(ctx, pair.parent, now)
		if err != nil || got != pair.want {
			t.Fatalf("ancestor replay: got=%s want=%s err=%v", got, pair.want, err)
		}
	}
	var count, eventCount int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM task_runs WHERE retry_of=$1`, run.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM run_events WHERE task_run_id IN (SELECT id FROM task_runs WHERE retry_of=$1) AND event_type='run.queued'`, run.ID).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if count != 2 || eventCount != 2 {
		t.Fatalf("extra retries/events: runs=%d events=%d", count, eventCount)
	}
}
