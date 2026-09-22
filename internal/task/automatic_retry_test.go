package task_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"yunling.local/platform/internal/agentprotocol"
	"yunling.local/platform/internal/scheduler"
	"yunling.local/platform/internal/task"
	"yunling.local/platform/internal/testpostgres"
)

func TestAutomaticRetryDurableIntentAndSafetyGuards(t *testing.T) {
	db := taskDatabase(t)
	testpostgres.ApplyMigration(t, db, "000007_run_observability.up.sql")
	ctx := context.Background()
	now := taskClock().Add(time.Hour)
	userID := insertTaskUser(t, db)
	scriptID := insertTaskScript(t, db, userID)
	_ = insertTaskVersion(t, db, scriptID, userID, 1)
	service := task.NewService(db, taskClock)
	events := task.NewEventService(task.NewPostgresRunEventStore(db))
	for _, tc := range []struct {
		name, eventType                               string
		idempotent, disabled, historical, unconfirmed bool
		cancelRequested                               bool
		retries, want                                 int
	}{
		{name: "failed", eventType: "failed", idempotent: true, retries: 1, want: 1},
		{name: "timed_out", eventType: "timed_out", idempotent: true, retries: 1, want: 1},
		{name: "cancelled", eventType: "cancelled", idempotent: true, retries: 1},
		{name: "cancel_raced_failure", eventType: "failed", idempotent: true, cancelRequested: true, retries: 1},
		{name: "succeeded", eventType: "succeeded", idempotent: true, retries: 1},
		{name: "not_idempotent", eventType: "failed", retries: 1},
		{name: "disabled", eventType: "failed", idempotent: true, disabled: true, retries: 1},
		{name: "historical", eventType: "failed", idempotent: true, historical: true, retries: 1},
		{name: "unconfirmed", eventType: "failed", idempotent: true, unconfirmed: true, retries: 1},
		{name: "no_retries", eventType: "failed", idempotent: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := validTaskInput(scriptID, userID, t.Name())
			input.Idempotent = tc.idempotent
			input.RetryPolicy = task.RetryPolicy{MaxRetries: tc.retries, BackoffSeconds: 30}
			definition, err := service.Create(ctx, input)
			if err != nil {
				t.Fatal(err)
			}
			run, err := service.Trigger(ctx, definition.ID, task.Trigger{Type: task.TriggerManual})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(ctx, `UPDATE task_runs SET state='assigned',execution_token=$2 WHERE id=$1`, run.ID, "retry-"+run.ID); err != nil {
				t.Fatal(err)
			}
			event := agentprotocol.RunEvent{RunID: run.ID, ExecutionToken: "retry-" + run.ID, Sequence: 1, Type: tc.eventType, OccurredAt: now, ExitCode: 7}
			if tc.cancelRequested {
				if _, err := db.Exec(ctx, `INSERT INTO run_events(task_run_id,sequence,event_type,state,payload,occurred_at) VALUES($1,1,'run.cancel_requested','assigned','{}',$2)`, run.ID, now); err != nil {
					t.Fatal(err)
				}
			}
			if tc.historical {
				// Replaying a terminal event saved by an old API cannot opt it in.
				if _, err := db.Exec(ctx, `UPDATE task_runs SET state='failed',process_confirmed_gone=true,finished_at=$2 WHERE id=$1`, run.ID, now); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(ctx, `INSERT INTO run_events(task_run_id,sequence,event_type,state,payload,occurred_at,execution_token,agent_sequence)
				VALUES($1,1,'run.failed','failed','{"message":"","exitCode":7}',$2,$3,1)`, run.ID, now, event.ExecutionToken); err != nil {
					t.Fatal(err)
				}
			} else if err := events.Apply(ctx, event); err != nil {
				t.Fatal(err)
			}
			if err := events.Apply(ctx, event); err != nil {
				t.Fatal(err)
			}
			if tc.disabled {
				if err := service.SetEnabled(ctx, definition.ID, false, true); err != nil {
					t.Fatal(err)
				}
			}
			if tc.unconfirmed {
				if _, err := db.Exec(ctx, `UPDATE task_runs SET process_confirmed_gone=false WHERE id=$1`, run.ID); err != nil {
					t.Fatal(err)
				}
			}
			// Fresh store instances simulate recovery after the API/worker dies
			// between the committed terminal event and retry consumption.
			var wg sync.WaitGroup
			for range 4 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if err := scheduler.NewPostgresStore(db).RetryFailed(ctx, now); err != nil {
						t.Error(err)
					}
				}()
			}
			wg.Wait()
			var count int
			if err := db.QueryRow(ctx, `SELECT count(*) FROM task_runs WHERE retry_of=$1`, run.ID).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != tc.want {
				t.Fatalf("retry count=%d want=%d", count, tc.want)
			}
			if tc.want == 0 {
				return
			}
			var child string
			var queuedAt time.Time
			if err := db.QueryRow(ctx, `SELECT id,queued_at FROM task_runs WHERE retry_of=$1`, run.ID).Scan(&child, &queuedAt); err != nil {
				t.Fatal(err)
			}
			if !queuedAt.Equal(now.Add(30 * time.Second)) {
				t.Fatalf("backoff=%v", queuedAt)
			}
			// The manual entry point racing with a worker must reuse this child.
			id, err := task.NewPostgresReconcileStore(db).RetryRun(ctx, task.RunID(run.ID), now)
			if err != nil || string(id) != child {
				t.Fatalf("manual/auto fork: %s %v", id, err)
			}
			if _, err := db.Exec(ctx, `UPDATE task_runs SET state='assigned',execution_token=$2 WHERE id=$1`, child, "retry-"+child); err != nil {
				t.Fatal(err)
			}
			if err := events.Apply(ctx, agentprotocol.RunEvent{RunID: child, ExecutionToken: "retry-" + child, Sequence: 1, Type: "failed", OccurredAt: now.Add(time.Minute)}); err != nil {
				t.Fatal(err)
			}
			if err := scheduler.NewPostgresStore(db).RetryFailed(ctx, now.Add(time.Minute)); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(ctx, `SELECT count(*) FROM task_runs WHERE retry_of=$1`, run.ID).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatalf("retry limit bypassed: %d", count)
			}
		})
	}
}
