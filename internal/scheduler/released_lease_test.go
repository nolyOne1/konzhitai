package scheduler_test

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"yunling.local/platform/internal/scheduler"
	"yunling.local/platform/internal/server"
	redisstore "yunling.local/platform/internal/store/redis"
	"yunling.local/platform/internal/task"
)

type releasedRuns struct {
	*memoryRuns
	released []scheduler.Lease
}

func (r *releasedRuns) ListReleasedLeases(context.Context, time.Time) ([]scheduler.Lease, error) {
	return r.released, nil
}

func TestScanReleasesCompletedReservationBeforeTTL(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	redis := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: redis.Addr()})
	defer client.Close()
	leases := redisstore.NewLeaseStore(client)
	resources := task.Resources{CPUMillicores: 1200, MemoryBytes: 128 << 20, DiskBytes: 128 << 20}
	available := task.Resources{CPUMillicores: 2000, MemoryBytes: 3 << 30, DiskBytes: 10 << 30}
	old, ok, err := leases.TryReserve(ctx, scheduler.LeaseRequest{RunID: "completed", ServerID: "server-a", Required: resources, Available: available, Now: now, TTL: 4 * time.Minute})
	if err != nil || !ok {
		t.Fatalf("reserve: %v %v", ok, err)
	}
	run := schedulableRun("waiting", 1200, 128<<20, 128<<20)
	run.QueuedAt, run.MaxWaitSeconds = now, 180
	runs := &releasedRuns{memoryRuns: newMemoryRuns(run)}
	item := schedulableServer("server-a")
	item.CPUAvailableMillicores = 2000
	svc := scheduler.NewService(runs, staticServers{items: []server.Snapshot{item}}, leases, func() time.Time { return now })
	if err := svc.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if len(runs.assignments) != 0 {
		t.Fatal("active reservation must block second run")
	}
	runs.released = []scheduler.Lease{old}
	redis.SetError("temporary outage")
	if err := svc.Scan(ctx); err == nil {
		t.Fatal("cleanup errors must be retried, not ignored")
	}
	if len(runs.assignments) != 0 {
		t.Fatal("must not assign after failed cleanup")
	}
	redis.SetError("")
	if err := svc.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if len(runs.assignments) != 1 {
		t.Fatal("completed reservation must release before TTL")
	}
	if err := svc.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if len(runs.assignments) != 1 {
		t.Fatal("cleanup must be idempotent")
	}
}

func TestReleasedLeaseQueryExcludesActiveAndExpired(t *testing.T) {
	db := schedulerDatabase(t)
	ctx := context.Background()
	now := time.Now().UTC()
	ids := seedSchedulingFixture(t, db, now)
	store := scheduler.NewPostgresStore(db)
	_, err := db.Exec(ctx, `INSERT INTO resource_leases(task_run_id,server_id,cpu_millicores,memory_bytes,disk_bytes,expires_at) VALUES($1,$2,1200,128,128,$3)`, ids.runID, ids.serverID, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	items, err := store.ListReleasedLeases(ctx, now)
	if err != nil || len(items) != 0 {
		t.Fatalf("active lease returned: %v %v", items, err)
	}
	if _, err := db.Exec(ctx, `UPDATE resource_leases SET released_at=$2 WHERE task_run_id=$1`, ids.runID, now); err != nil {
		t.Fatal(err)
	}
	items, err = store.ListReleasedLeases(ctx, now)
	if err != nil || len(items) != 1 || items[0].RunID != ids.runID || items[0].ServerID != ids.serverID {
		t.Fatalf("released lease missing: %v %v", items, err)
	}
	items, err = store.ListReleasedLeases(ctx, now.Add(time.Minute))
	if err != nil || len(items) != 0 {
		t.Fatalf("expired lease returned: %v %v", items, err)
	}
}
