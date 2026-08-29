package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/jackc/pgx/v5/pgxpool"
	goredis "github.com/redis/go-redis/v9"
	"yunling.local/platform/internal/scheduler"
	redisstore "yunling.local/platform/internal/store/redis"
	"yunling.local/platform/internal/task"
	"yunling.local/platform/internal/testpostgres"
)

func TestSchedulerRestoresDatabaseLeasesBeforeWakingQueue(t *testing.T) {
	db := recoveryDatabase(t)
	redisServer := miniredis.RunT(t)
	redisClient := goredis.NewClient(&goredis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = redisClient.Close() })
	ctx := context.Background()
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	ids := seedRecoveryFixture(t, db, now)
	store := scheduler.NewPostgresStore(db)
	leases := redisstore.NewLeaseStore(redisClient)
	service := scheduler.NewService(store, store, leases, func() time.Time { return now })

	if err := service.Scan(ctx); err != nil {
		t.Fatalf("调度器恢复扫描：%v", err)
	}
	var state task.RunState
	if err := db.QueryRow(ctx, `SELECT state FROM task_runs WHERE id=$1`, ids.waitingRunID).Scan(&state); err != nil {
		t.Fatalf("读取等待任务状态：%v", err)
	}
	if state != task.Queued {
		t.Fatalf("活动租约恢复后资源不足，任务必须继续排队，实际=%s", state)
	}

	if _, err := db.Exec(ctx, `UPDATE resource_leases SET released_at=$2 WHERE id=$1`, ids.lease.ID, now); err != nil {
		t.Fatalf("释放数据库资源租约：%v", err)
	}
	if _, err := db.Exec(ctx, `UPDATE task_runs SET state='succeeded', finished_at=$2, updated_at=$2 WHERE id=$1`, ids.runningRunID, now); err != nil {
		t.Fatalf("结束原运行实例：%v", err)
	}
	if err := leases.Release(ctx, ids.lease); err != nil {
		t.Fatalf("释放 Redis 资源租约：%v", err)
	}
	if err := service.Scan(ctx); err != nil {
		t.Fatalf("资源释放后唤醒队列：%v", err)
	}
	if err := db.QueryRow(ctx, `SELECT state FROM task_runs WHERE id=$1`, ids.waitingRunID).Scan(&state); err != nil {
		t.Fatalf("读取唤醒后的任务状态：%v", err)
	}
	if state != task.Assigned {
		t.Fatalf("资源释放后排队任务应自动分配，实际=%s", state)
	}
}

type recoveryIDs struct {
	runningRunID string
	waitingRunID string
	lease        scheduler.Lease
}

func recoveryDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()
	db := testpostgres.Start(t)
	for _, migration := range []string{
		"000001_initial.up.sql", "000002_agent_enrollment.up.sql", "000003_server_management.up.sql",
		"000004_script_sync_states.up.sql", "000005_task_scheduling.up.sql", "000006_scheduler_resources.up.sql",
		"000007_run_observability.up.sql",
	} {
		testpostgres.ApplyMigration(t, db, migration)
	}
	return db
}

func seedRecoveryFixture(t *testing.T, db *pgxpool.Pool, now time.Time) recoveryIDs {
	t.Helper()
	const (
		scriptID     = "10000000-0000-4000-8000-000000000001"
		versionID    = "10000000-0000-4000-8000-000000000002"
		definitionID = "10000000-0000-4000-8000-000000000003"
		serverID     = "10000000-0000-4000-8000-000000000004"
		runningRunID = "10000000-0000-4000-8000-000000000005"
		waitingRunID = "10000000-0000-4000-8000-000000000006"
		leaseID      = "10000000-0000-4000-8000-000000000007"
	)
	ctx := context.Background()
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO scripts (id,name,runtime) VALUES ($1,'恢复测试脚本','bash')`, []any{scriptID}},
		{`INSERT INTO script_versions (id,script_id,version,artifact_uri,artifact_sha256,entrypoint) VALUES ($1,$2,1,'s3://test/recovery',repeat('a',64),'main.sh')`, []any{versionID, scriptID}},
		{`INSERT INTO task_definitions (id,name,script_id,required_runtime,cpu_millicores,memory_bytes,disk_bytes,max_concurrency,max_wait_seconds) VALUES ($1,'恢复测试任务',$2,'bash',2000,$3,$4,2,3600)`, []any{definitionID, scriptID, int64(2 << 30), int64(2 << 30)}},
		{`INSERT INTO servers (id,name,status,labels,runtimes,max_concurrency,enabled,drain_requested,scheduling_weight) VALUES ($1,'恢复测试节点','online','{}','["bash"]',4,true,false,100)`, []any{serverID}},
		{`INSERT INTO server_snapshots (server_id,cpu_usage_percent,cpu_total_milli,cpu_used_milli,memory_total_bytes,memory_available_bytes,memory_used_bytes,disk_total_bytes,disk_available_bytes,disk_free_bytes,running_tasks,collected_at) VALUES ($1,0,4000,0,$2,$2,0,$3,$3,$3,1,$4)`, []any{serverID, int64(4 << 30), int64(4 << 30), now}},
		{`INSERT INTO script_syncs (server_id,script_version_id,status,artifact_sha256,synced_at) VALUES ($1,$2,'ready',repeat('a',64),$3)`, []any{serverID, versionID, now}},
		{`INSERT INTO task_runs (id,task_definition_id,script_version_id,assigned_server_id,trigger_type,state,queued_at,assigned_at,priority,cpu_millicores,memory_bytes,disk_bytes,max_concurrency,timeout_seconds,max_wait_seconds,required_labels,required_runtime) VALUES ($1,$2,$3,$4,'manual','running',$5,$5,80,3000,$6,$6,2,1800,3600,'{}','bash')`, []any{runningRunID, definitionID, versionID, serverID, now.Add(-time.Minute), int64(3 << 30)}},
		{`INSERT INTO task_runs (id,task_definition_id,script_version_id,trigger_type,state,queued_at,priority,cpu_millicores,memory_bytes,disk_bytes,max_concurrency,timeout_seconds,max_wait_seconds,required_labels,required_runtime) VALUES ($1,$2,$3,'manual','queued',$4,80,2000,$5,$5,2,1800,3600,'{}','bash')`, []any{waitingRunID, definitionID, versionID, now.Add(-30 * time.Second), int64(2 << 30)}},
		{`INSERT INTO resource_leases (id,task_run_id,server_id,cpu_millicores,memory_bytes,disk_bytes,expires_at,created_at) VALUES ($1,$2,$3,3000,$4,$4,$5,$6)`, []any{leaseID, runningRunID, serverID, int64(3 << 30), now.Add(time.Hour), now.Add(-time.Minute)}},
	}
	for _, statement := range statements {
		if _, err := db.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatalf("准备调度恢复数据：%v\nSQL: %s", err, statement.query)
		}
	}
	return recoveryIDs{
		runningRunID: runningRunID,
		waitingRunID: waitingRunID,
		lease: scheduler.Lease{
			ID: leaseID, RunID: runningRunID, ServerID: serverID,
			Resources: task.Resources{CPUMillicores: 3000, MemoryBytes: 3 << 30, DiskBytes: 3 << 30},
			ExpiresAt: now.Add(time.Hour),
		},
	}
}
