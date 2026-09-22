package scheduler_test

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"yunling.local/platform/internal/agentprotocol"
	"yunling.local/platform/internal/scheduler"
	"yunling.local/platform/internal/server"
	redisstore "yunling.local/platform/internal/store/redis"
	"yunling.local/platform/internal/task"
)

func TestRetryBackoffDoesNotReserveOrConsumeQueueDeadline(t *testing.T) {
	now := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	due := now.Add(30 * time.Second)
	run := schedulableRun("retry-backoff", 100, 128, 128)
	run.TriggerType, run.QueuedAt, run.MaxWaitSeconds = task.TriggerRetry, due, 5
	runs := newMemoryRuns(run)
	svc := scheduler.NewService(runs, staticServers{items: []server.Snapshot{schedulableServer("server-a")}}, newMemoryLeases(task.Resources{CPUMillicores: 4000, MemoryBytes: 4 << 30, DiskBytes: 4 << 30}), func() time.Time { return now })
	// Repeated scans and direct wakeups must both respect the same barrier.
	for _, offset := range []time.Duration{0, 29 * time.Second} {
		now = due.Add(-30 * time.Second).Add(offset)
		if err := svc.Scan(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.ScheduleOne(context.Background(), run.ID); err != nil {
			t.Fatal(err)
		}
		if len(runs.assignments) != 0 || runs.mustGet(run.ID).State != task.Queued {
			t.Fatal("retry started/expired during backoff")
		}
	}
	now = due
	if outcome, err := svc.ScheduleOne(context.Background(), run.ID); err != nil || outcome != scheduler.OutcomeAssigned {
		t.Fatalf("due retry: %s %v", outcome, err)
	}
}

func TestPostgresScanRecoversAutomaticRetryAndHonorsBackoff(t *testing.T) {
	db := schedulerDatabase(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	ids := seedSchedulingFixture(t, db, now)
	if _, err := db.Exec(ctx, `UPDATE task_runs SET state='assigned',execution_token='retry-token',idempotent=true,max_retries=1,retry_backoff_seconds=30 WHERE id=$1`, ids.runID); err != nil {
		t.Fatal(err)
	}
	if err := task.NewEventService(task.NewPostgresRunEventStore(db)).Apply(ctx, agentprotocol.RunEvent{RunID: ids.runID, ExecutionToken: "retry-token", Sequence: 1, Type: "failed", OccurredAt: now}); err != nil {
		t.Fatal(err)
	}
	store := scheduler.NewPostgresStore(db)
	redis := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: redis.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	leases := redisstore.NewLeaseStore(client)
	svc := scheduler.NewService(store, store, leases, func() time.Time { return now })
	if err := svc.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	var child string
	if err := db.QueryRow(ctx, `SELECT id FROM task_runs WHERE retry_of=$1`, ids.runID).Scan(&child); err != nil {
		t.Fatal(err)
	}
	// Even bypassing ScheduleOne must not create a database reservation early.
	if assigned, err := store.Assign(ctx, scheduler.Assignment{RunID: child, ServerID: ids.serverID, AssignedAt: now}); err != nil || assigned {
		t.Fatalf("early direct assignment=%v err=%v", assigned, err)
	}
	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM resource_leases WHERE task_run_id=$1`, child).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("backoff reserved resources")
	}
	now = now.Add(30 * time.Second)
	// A new scheduler, with an empty in-memory queue, recovers due work.
	if err := scheduler.NewService(store, store, leases, func() time.Time { return now }).Scan(ctx); err != nil {
		t.Fatal(err)
	}
	run, err := store.Get(ctx, child)
	if err != nil || run.State != task.Assigned {
		t.Fatalf("due child not assigned: %+v %v", run, err)
	}
}
