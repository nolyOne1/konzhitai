package dispatch_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"yunling.local/platform/internal/dispatch"
	"yunling.local/platform/internal/testpostgres"
)

const (
	dispatchScriptID     = "20000000-0000-4000-8000-000000000001"
	dispatchVersionID    = "20000000-0000-4000-8000-000000000002"
	dispatchDefinitionID = "20000000-0000-4000-8000-000000000003"
	dispatchServerID     = "20000000-0000-4000-8000-000000000004"
	dispatchDueRunID     = "20000000-0000-4000-8000-000000000005"
	dispatchRecentRunID  = "20000000-0000-4000-8000-000000000006"
)

func TestPostgresStoreClaimsOnlyDueAssignedRuns(t *testing.T) {
	db := dispatchDatabase(t)
	now := time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)
	seedDispatchRuns(t, db, now)
	store := dispatch.NewPostgresStore(db)

	runs, err := store.Claim(context.Background(), now.Add(-10*time.Second), now, 20)
	if err != nil {
		t.Fatalf("领取待派发运行：%v", err)
	}
	if len(runs) != 1 || runs[0].ID != dispatchDueRunID {
		t.Fatalf("只能领取到期运行：%+v；最近运行=%s", runs, dispatchRecentRunID)
	}
	run := runs[0]
	if run.ExecutionToken != "token-due" || run.ScriptID != dispatchScriptID || run.ScriptVersionID != dispatchVersionID || run.Entrypoint != "main.sh" {
		t.Fatalf("执行载荷不完整：%+v", run)
	}
	if run.Runtime != "bash" || run.Parameters["日期"] != "2026-08-29" || run.SecretBindings["访问令牌"] != "secret-1" {
		t.Fatalf("执行参数快照不完整：%+v", run)
	}
	if run.Resources.CPUMillicores != 100 || run.Resources.MemoryBytes != 64<<20 || run.Resources.DiskBytes != 16<<20 || run.Timeout != time.Minute || run.Attempt != 1 {
		t.Fatalf("资源、超时或派发次数错误：%+v", run)
	}
}

func TestPostgresStoreConcurrentClaimReturnsRunOnce(t *testing.T) {
	db := dispatchDatabase(t)
	now := time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)
	seedDispatchRuns(t, db, now)
	store := dispatch.NewPostgresStore(db)

	results := make(chan []dispatch.Run, 2)
	errors := make(chan error, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			runs, err := store.Claim(context.Background(), now.Add(-10*time.Second), now, 20)
			if err != nil {
				errors <- err
				return
			}
			results <- runs
		}()
	}
	workers.Wait()
	close(results)
	close(errors)
	for err := range errors {
		t.Fatalf("并发领取运行：%v", err)
	}
	claimed := 0
	for runs := range results {
		for _, run := range runs {
			if run.ID == dispatchDueRunID {
				claimed++
			}
		}
	}
	if claimed != 1 {
		t.Fatalf("同一运行只能被领取一次，实际=%d", claimed)
	}
}

func TestPostgresStoreRecordsResultOnlyForMatchingAssignedRun(t *testing.T) {
	db := dispatchDatabase(t)
	now := time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)
	seedDispatchRuns(t, db, now)
	store := dispatch.NewPostgresStore(db)
	if _, err := store.Claim(context.Background(), now.Add(-10*time.Second), now, 20); err != nil {
		t.Fatalf("领取待派发运行：%v", err)
	}

	if err := store.RecordResult(context.Background(), dispatchDueRunID, "token-due", "代理连接中断"); err != nil {
		t.Fatalf("记录派发结果：%v", err)
	}
	var message string
	if err := db.QueryRow(context.Background(), `SELECT dispatch_error FROM task_runs WHERE id=$1`, dispatchDueRunID).Scan(&message); err != nil {
		t.Fatalf("读取派发错误：%v", err)
	}
	if message != "代理连接中断" {
		t.Fatalf("派发错误未保存：%q", message)
	}

	if _, err := db.Exec(context.Background(), `UPDATE task_runs SET state='running' WHERE id=$1`, dispatchDueRunID); err != nil {
		t.Fatalf("推进运行状态：%v", err)
	}
	if err := store.RecordResult(context.Background(), dispatchDueRunID, "token-due", "不应写入"); err != nil {
		t.Fatalf("终态保护不应返回错误：%v", err)
	}
	if err := db.QueryRow(context.Background(), `SELECT dispatch_error FROM task_runs WHERE id=$1`, dispatchDueRunID).Scan(&message); err != nil {
		t.Fatalf("重新读取派发错误：%v", err)
	}
	if message != "代理连接中断" {
		t.Fatalf("非 assigned 运行不得被覆盖：%q", message)
	}
}

func dispatchDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()
	db := testpostgres.Start(t)
	for _, migration := range []string{
		"000001_initial.up.sql", "000002_agent_enrollment.up.sql", "000003_server_management.up.sql",
		"000004_script_sync_states.up.sql", "000005_task_scheduling.up.sql", "000006_scheduler_resources.up.sql",
		"000007_run_observability.up.sql", "000008_security_audit_alerts.up.sql", "000009_run_dispatch.up.sql",
	} {
		testpostgres.ApplyMigration(t, db, migration)
	}
	return db
}

func seedDispatchRuns(t *testing.T, db *pgxpool.Pool, now time.Time) {
	t.Helper()
	ctx := context.Background()
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO scripts (id,name,runtime) VALUES ($1,'派发测试脚本','bash')`, []any{dispatchScriptID}},
		{`INSERT INTO script_versions (id,script_id,version,artifact_uri,artifact_sha256,entrypoint,manifest) VALUES ($1,$2,1,'scripts/test.tar.gz',repeat('a',64),'main.sh','{"runtime":"bash","entrypoint":"main.sh"}')`, []any{dispatchVersionID, dispatchScriptID}},
		{`INSERT INTO task_definitions (id,name,script_id,version_policy,pinned_version_id,parameters,secret_bindings,required_runtime,cpu_millicores,memory_bytes,disk_bytes,timeout_seconds) VALUES ($1,'派发测试任务',$2,'pinned',$3,'{"日期":"2026-08-29"}','{"访问令牌":"secret-1"}','bash',100,$4,$5,60)`, []any{dispatchDefinitionID, dispatchScriptID, dispatchVersionID, int64(64 << 20), int64(16 << 20)}},
		{`INSERT INTO servers (id,name,status,runtimes) VALUES ($1,'派发测试节点','online','["bash"]')`, []any{dispatchServerID}},
		{`INSERT INTO task_runs (id,task_definition_id,script_version_id,assigned_server_id,trigger_type,state,parameters_snapshot,queued_at,assigned_at,execution_token,required_labels,required_runtime,cpu_millicores,memory_bytes,disk_bytes,timeout_seconds) VALUES ($1,$2,$3,$4,'manual','assigned','{"日期":"2026-08-29"}',$5,$5,'token-due','{}','bash',100,$6,$7,60)`, []any{dispatchDueRunID, dispatchDefinitionID, dispatchVersionID, dispatchServerID, now.Add(-time.Minute), int64(64 << 20), int64(16 << 20)}},
		{`INSERT INTO task_runs (id,task_definition_id,script_version_id,assigned_server_id,trigger_type,state,parameters_snapshot,queued_at,assigned_at,execution_token,required_labels,required_runtime,cpu_millicores,memory_bytes,disk_bytes,timeout_seconds,last_dispatch_at) VALUES ($1,$2,$3,$4,'manual','assigned','{}',$5,$5,'token-recent','{}','bash',100,$6,$7,60,$8)`, []any{dispatchRecentRunID, dispatchDefinitionID, dispatchVersionID, dispatchServerID, now.Add(-time.Minute), int64(64 << 20), int64(16 << 20), now.Add(-time.Second)}},
	}
	for _, statement := range statements {
		if _, err := db.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatalf("准备派发测试数据：%v\nSQL: %s", err, statement.query)
		}
	}
}
