package scheduler_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"yunling.local/platform/internal/scheduler"
	"yunling.local/platform/internal/task"
	"yunling.local/platform/internal/testpostgres"
)

func TestPostgresStoreLoadsSchedulingSnapshotAndAssignsOnce(t *testing.T) {
	db := schedulerDatabase(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	ids := seedSchedulingFixture(t, db, now)
	store := scheduler.NewPostgresStore(db)
	if _, err := db.Exec(ctx, `UPDATE task_definitions SET required_runtime='python3', required_labels='{"用途":"交互"}' WHERE id=$1`, ids.definitionID); err != nil {
		t.Fatalf("修改任务定义：%v", err)
	}

	run, err := store.Get(ctx, ids.runID)
	if err != nil {
		t.Fatalf("读取排队运行实例：%v", err)
	}
	if run.RequiredRuntime != "bash" || run.RequiredLabels["用途"] != "批处理" || run.Resources.CPUMillicores != 2000 {
		t.Fatalf("运行实例必须包含分配条件和资源快照：%+v", run)
	}
	snapshots, err := store.Snapshots(ctx, run)
	if err != nil {
		t.Fatalf("读取服务器调度快照：%v", err)
	}
	if len(snapshots) != 1 {
		t.Fatalf("应读取一个服务器快照，实际=%d", len(snapshots))
	}
	item := snapshots[0]
	if item.CPUTotalMillicores != 8000 || item.CPUAvailableMillicores != 5000 || !item.ReadyScriptVersions[ids.versionID] || item.BlockedScriptVersions[ids.versionID] {
		t.Fatalf("服务器容量或脚本缓存状态错误：%+v", item)
	}

	lease := scheduler.Lease{
		ID: "77777777-7777-4777-8777-777777777777", RunID: ids.runID, ServerID: ids.serverID,
		Resources: run.Resources, ExpiresAt: now.Add(time.Hour),
	}
	assignment := scheduler.Assignment{RunID: ids.runID, ServerID: ids.serverID, ScriptVersionID: ids.versionID, Lease: lease, AssignedAt: now}
	assigned, err := store.Assign(ctx, assignment)
	if err != nil || !assigned {
		t.Fatalf("首次分配应成功，assigned=%v err=%v", assigned, err)
	}
	assigned, err = store.Assign(ctx, assignment)
	if err != nil || assigned {
		t.Fatalf("同一运行实例不得重复分配，assigned=%v err=%v", assigned, err)
	}
	var state task.RunState
	var serverID, eventType string
	var leaseCount int
	if err := db.QueryRow(ctx, `SELECT state, assigned_server_id FROM task_runs WHERE id=$1`, ids.runID).Scan(&state, &serverID); err != nil {
		t.Fatalf("读取分配结果：%v", err)
	}
	if err := db.QueryRow(ctx, `SELECT event_type FROM run_events WHERE task_run_id=$1 ORDER BY sequence DESC LIMIT 1`, ids.runID).Scan(&eventType); err != nil {
		t.Fatalf("读取分配事件：%v", err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM resource_leases WHERE task_run_id=$1 AND released_at IS NULL`, ids.runID).Scan(&leaseCount); err != nil {
		t.Fatalf("读取数据库资源租约：%v", err)
	}
	if state != task.Assigned || serverID != ids.serverID || eventType != "run.assigned" || leaseCount != 1 {
		t.Fatalf("分配事务不完整：state=%s server=%s event=%s leases=%d", state, serverID, eventType, leaseCount)
	}
}

func TestPostgresStoreExpiresQueuedRunWithEvent(t *testing.T) {
	db := schedulerDatabase(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	ids := seedSchedulingFixture(t, db, now)
	store := scheduler.NewPostgresStore(db)

	expired, err := store.Expire(ctx, ids.runID, now)
	if err != nil || !expired {
		t.Fatalf("过期排队任务：expired=%v err=%v", expired, err)
	}
	var state task.RunState
	var eventType string
	if err := db.QueryRow(ctx, `SELECT state FROM task_runs WHERE id=$1`, ids.runID).Scan(&state); err != nil {
		t.Fatalf("读取过期状态：%v", err)
	}
	if err := db.QueryRow(ctx, `SELECT event_type FROM run_events WHERE task_run_id=$1 ORDER BY sequence DESC LIMIT 1`, ids.runID).Scan(&eventType); err != nil {
		t.Fatalf("读取过期事件：%v", err)
	}
	if state != task.Expired || eventType != "run.expired" {
		t.Fatalf("过期状态和事件必须原子写入，state=%s event=%s", state, eventType)
	}
}

func TestPostgresStoreAtomicallyEnforcesDefinitionConcurrency(t *testing.T) {
	db := schedulerDatabase(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	ids := seedSchedulingFixture(t, db, now)
	if _, err := db.Exec(ctx, `UPDATE task_runs SET max_concurrency=1 WHERE id=$1`, ids.runID); err != nil {
		t.Fatalf("设置任务最大并发：%v", err)
	}
	secondRunID := "66666666-6666-4666-8666-666666666666"
	if _, err := db.Exec(ctx, `
		INSERT INTO task_runs (
			id,task_definition_id,script_version_id,trigger_type,state,queued_at,
			priority,cpu_millicores,memory_bytes,disk_bytes,max_concurrency,
			timeout_seconds,max_wait_seconds,required_labels,required_runtime
		)
		SELECT $1,task_definition_id,script_version_id,trigger_type,'queued',$2,
		       priority,cpu_millicores,memory_bytes,disk_bytes,1,
		       timeout_seconds,max_wait_seconds,required_labels,required_runtime
		FROM task_runs WHERE id=$3
	`, secondRunID, now.Add(-30*time.Second), ids.runID); err != nil {
		t.Fatalf("创建第二个排队实例：%v", err)
	}
	store := scheduler.NewPostgresStore(db)
	start := make(chan struct{})
	results := make(chan bool, 2)
	var wait sync.WaitGroup
	for index, runID := range []string{ids.runID, secondRunID} {
		wait.Add(1)
		go func(index int, runID string) {
			defer wait.Done()
			<-start
			leaseID := []string{"77777777-7777-4777-8777-777777777771", "77777777-7777-4777-8777-777777777772"}[index]
			assigned, err := store.Assign(ctx, scheduler.Assignment{
				RunID: runID, ServerID: ids.serverID, ScriptVersionID: ids.versionID, AssignedAt: now,
				Lease: scheduler.Lease{ID: leaseID, RunID: runID, ServerID: ids.serverID,
					Resources: task.Resources{CPUMillicores: 2000, MemoryBytes: 2 << 30, DiskBytes: 4 << 30}, ExpiresAt: now.Add(time.Hour)},
			})
			if err != nil {
				t.Errorf("并发分配任务：%v", err)
				return
			}
			results <- assigned
		}(index, runID)
	}
	close(start)
	wait.Wait()
	close(results)
	winners := 0
	for assigned := range results {
		if assigned {
			winners++
		}
	}
	var active int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM task_runs WHERE task_definition_id=$1 AND state='assigned'`, ids.definitionID).Scan(&active); err != nil {
		t.Fatalf("统计并发分配结果：%v", err)
	}
	if winners != 1 || active != 1 {
		t.Fatalf("最大并发为 1 时只能有一个分配成功，winners=%d active=%d", winners, active)
	}
}

type schedulingIDs struct{ runID, definitionID, versionID, serverID string }

func schedulerDatabase(t *testing.T) *pgxpool.Pool {
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

func seedSchedulingFixture(t *testing.T, db *pgxpool.Pool, now time.Time) schedulingIDs {
	t.Helper()
	ids := schedulingIDs{
		runID: "11111111-1111-4111-8111-111111111111", definitionID: "22222222-2222-4222-8222-222222222222",
		versionID: "33333333-3333-4333-8333-333333333333", serverID: "44444444-4444-4444-8444-444444444444",
	}
	scriptID := "55555555-5555-4555-8555-555555555555"
	ctx := context.Background()
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO scripts (id,name,runtime) VALUES ($1,'调度测试脚本','bash')`, []any{scriptID}},
		{`INSERT INTO script_versions (id,script_id,version,artifact_uri,artifact_sha256,entrypoint) VALUES ($1,$2,1,'s3://test/script',repeat('a',64),'main.sh')`, []any{ids.versionID, scriptID}},
		{`INSERT INTO task_definitions (id,name,script_id,required_runtime,required_labels,cpu_millicores,memory_bytes,disk_bytes,max_concurrency,max_wait_seconds) VALUES ($1,'调度测试任务',$2,'bash','{"用途":"批处理"}',2000,$3,$4,2,3600)`, []any{ids.definitionID, scriptID, int64(2 << 30), int64(4 << 30)}},
		{`INSERT INTO task_runs (id,task_definition_id,script_version_id,trigger_type,state,queued_at,priority,cpu_millicores,memory_bytes,disk_bytes,max_concurrency,timeout_seconds,max_wait_seconds,required_labels,required_runtime) VALUES ($1,$2,$3,'manual','queued',$4,80,2000,$5,$6,2,1800,3600,'{"用途":"批处理"}','bash')`, []any{ids.runID, ids.definitionID, ids.versionID, now.Add(-time.Minute), int64(2 << 30), int64(4 << 30)}},
		{`INSERT INTO run_events (task_run_id,sequence,event_type,state,payload,occurred_at) VALUES ($1,0,'run.queued','queued','{}',$2)`, []any{ids.runID, now.Add(-time.Minute)}},
		{`INSERT INTO servers (id,name,status,labels,runtimes,max_concurrency,enabled,drain_requested,scheduling_weight) VALUES ($1,'调度节点','online','{"用途":"批处理"}','["bash"]',4,true,false,100)`, []any{ids.serverID}},
		{`INSERT INTO server_snapshots (server_id,cpu_usage_percent,cpu_total_milli,cpu_used_milli,memory_total_bytes,memory_available_bytes,memory_used_bytes,disk_total_bytes,disk_available_bytes,disk_free_bytes,running_tasks,collected_at) VALUES ($1,37.5,8000,3000,$2,$3,$4,$5,$6,$6,1,$7)`, []any{ids.serverID, int64(16 << 30), int64(12 << 30), int64(4 << 30), int64(100 << 30), int64(80 << 30), now}},
		{`INSERT INTO script_syncs (server_id,script_version_id,status,artifact_sha256,synced_at) VALUES ($1,$2,'ready',repeat('a',64),$3)`, []any{ids.serverID, ids.versionID, now}},
	}
	for _, statement := range statements {
		if _, err := db.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatalf("准备调度数据库：%v\nSQL: %s", err, statement.query)
		}
	}
	return ids
}
