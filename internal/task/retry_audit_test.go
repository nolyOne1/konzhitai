package task_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"yunling.local/platform/internal/agentprotocol"
	"yunling.local/platform/internal/scheduler"
	"yunling.local/platform/internal/task"
	"yunling.local/platform/internal/testpostgres"
)

// Safety regressions. All data belongs to embedded PostgreSQL.
func TestRetryAudit(t *testing.T) {
	db := taskDatabase(t)
	testpostgres.ApplyMigration(t, db, "000007_run_observability.up.sql")
	ctx := context.Background()
	now := taskClock().Add(time.Hour)
	userID := insertTaskUser(t, db)
	scriptID := insertTaskScript(t, db, userID)
	_ = insertTaskVersion(t, db, scriptID, userID, 1)
	service := task.NewService(db, taskClock)
	store := task.NewPostgresReconcileStore(db)
	newRun := func(t *testing.T, state task.RunState, idempotent, gone bool, retries, backoff int) string {
		t.Helper()
		input := validTaskInput(scriptID, userID, t.Name())
		input.Idempotent = idempotent
		input.RetryPolicy = task.RetryPolicy{MaxRetries: retries, BackoffSeconds: backoff}
		definition, err := service.Create(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		run, err := service.Trigger(ctx, definition.ID, task.Trigger{Type: task.TriggerManual, RequestedBy: userID})
		if err != nil {
			t.Fatal(err)
		}
		_, err = db.Exec(ctx, `UPDATE task_runs SET state=$2, process_confirmed_gone=$3, finished_at=$4 WHERE id=$1`, run.ID, state, gone, now)
		if err != nil {
			t.Fatal(err)
		}
		return run.ID
	}
	for _, tc := range []struct {
		name             string
		state            task.RunState
		idempotent, gone bool
		retries          int
	}{
		{"non_idempotent", task.Failed, false, true, 1},
		{"process_unconfirmed", task.Unknown, true, false, 1},
		{"zero_retries", task.Failed, true, true, 0},
		{"successful_run", task.Succeeded, true, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := newRun(t, tc.state, tc.idempotent, tc.gone, tc.retries, 0)
			if _, err := store.RetryRun(ctx, task.RunID(id), now); !errors.Is(err, task.ErrRunNotRetryable) {
				t.Fatalf("unsafe retry accepted: %v", err)
			}
		})
	}
	t.Run("retry_chain_limit", func(t *testing.T) {
		id := newRun(t, task.Failed, true, true, 1, 0)
		child, err := store.RetryRun(ctx, task.RunID(id), now)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(ctx, `UPDATE task_runs SET state='failed',process_confirmed_gone=true WHERE id=$1`, child); err != nil {
			t.Fatal(err)
		}
		if _, err := store.RetryRun(ctx, child, now); !errors.Is(err, task.ErrRunNotRetryable) {
			t.Fatalf("attempt limit not enforced: %v", err)
		}
	})
	t.Run("duplicate_requests_share_one_retry", func(t *testing.T) {
		id := newRun(t, task.Failed, true, true, 1, 0)
		var wg sync.WaitGroup
		start := make(chan struct{})
		errs := make(chan error, 2)
		for range 2 {
			wg.Add(1)
			go func() { defer wg.Done(); <-start; _, err := store.RetryRun(ctx, task.RunID(id), now); errs <- err }()
		}
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil && !errors.Is(err, task.ErrRunNotRetryable) {
				t.Fatal(err)
			}
		}
		var count int
		if err := db.QueryRow(ctx, `SELECT count(*) FROM task_runs WHERE retry_of=$1`, id).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("max_retries=1 but repeated requests created %d child runs", count)
		}
	})
	t.Run("backoff_prevents_immediate_queue_eligibility", func(t *testing.T) {
		id := newRun(t, task.Failed, true, true, 1, 30)
		child, err := store.RetryRun(ctx, task.RunID(id), now)
		if errors.Is(err, task.ErrRunNotRetryable) {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		runs, err := scheduler.NewPostgresStore(db).ListQueued(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, run := range runs {
			if run.ID == string(child) && !run.QueuedAt.After(now) && run.ScheduledFor == nil {
				t.Fatalf("30s backoff ignored: retry is immediately queued at %s", run.QueuedAt.Format(time.RFC3339))
			}
		}
	})
	t.Run("confirmed_absent_retry_releases_old_lease", func(t *testing.T) {
		id := newRun(t, task.Unknown, true, false, 1, 0)
		var serverID string
		if err := db.QueryRow(ctx, `INSERT INTO servers(name,status) VALUES('retry-audit','online') RETURNING id`).Scan(&serverID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(ctx, `UPDATE task_runs SET assigned_server_id=$2,execution_token='audit-lease-token',finished_at=NULL WHERE id=$1`, id, serverID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(ctx, `INSERT INTO resource_leases(task_run_id,server_id,cpu_millicores,memory_bytes,disk_bytes,expires_at) VALUES($1,$2,100,128,128,$3)`, id, serverID, now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		for _, report := range []agentprotocol.RunningReport{
			{ServerID: serverID},
			{ServerID: serverID, Authoritative: true, Processes: []agentprotocol.RunningProcess{{RunID: id, ExecutionToken: "audit-lease-token"}}},
		} {
			if err := store.ReconcileRunning(ctx, report, now); err != nil {
				t.Fatal(err)
			}
			var retained int
			if err := db.QueryRow(ctx, `SELECT count(*) FROM resource_leases WHERE task_run_id=$1 AND released_at IS NULL`, id).Scan(&retained); err != nil {
				t.Fatal(err)
			}
			if retained != 1 {
				t.Fatal("live/unconfirmed process lost its lease")
			}
		}
		if err := store.ReconcileRunning(ctx, agentprotocol.RunningReport{ServerID: serverID, Authoritative: true}, now); err != nil {
			t.Fatal(err)
		}
		if err := store.ReconcileRunning(ctx, agentprotocol.RunningReport{ServerID: serverID, Authoritative: true}, now.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		var absentEvents int
		var confirmedAt time.Time
		if err := db.QueryRow(ctx, `SELECT updated_at FROM task_runs WHERE id=$1`, id).Scan(&confirmedAt); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(ctx, `SELECT count(*) FROM run_events WHERE task_run_id=$1 AND event_type='run.process_absent'`, id).Scan(&absentEvents); err != nil {
			t.Fatal(err)
		}
		if absentEvents != 1 || !confirmedAt.Equal(now) {
			t.Fatal("duplicate report changed absence/backoff origin")
		}
		if _, err := store.RetryRun(ctx, task.RunID(id), now); err != nil {
			t.Fatal(err)
		}
		var remaining int
		if err := db.QueryRow(ctx, `SELECT count(*) FROM resource_leases WHERE task_run_id=$1 AND released_at IS NULL`, id).Scan(&remaining); err != nil {
			t.Fatal(err)
		}
		if remaining != 0 {
			t.Fatalf("confirmed absent process retried but %d old leases remain unreleased", remaining)
		}
	})
}
