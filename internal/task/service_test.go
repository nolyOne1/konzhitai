package task_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"yunling.local/platform/internal/task"
	"yunling.local/platform/internal/testpostgres"
)

func TestTriggerResolvesLatestVersionOnce(t *testing.T) {
	db := taskDatabase(t)
	ctx := context.Background()
	userID := insertTaskUser(t, db)
	scriptID := insertTaskScript(t, db, userID)
	versionOneID := insertTaskVersion(t, db, scriptID, userID, 1)
	service := task.NewService(db, taskClock)
	definition, err := service.Create(ctx, task.CreateInput{
		Name:            "每日数据归档",
		Description:     "归档业务数据",
		ScriptID:        scriptID,
		VersionPolicy:   task.VersionLatest,
		Parameters:      map[string]any{"保留天数": float64(30)},
		SecretRefs:      map[string]string{"访问令牌": "archive-token"},
		RequiredLabels:  map[string]string{"用途": "批处理"},
		RequiredRuntime: "bash",
		Resources:       task.Resources{CPUMillicores: 250, MemoryBytes: 256 << 20, DiskBytes: 512 << 20},
		Priority:        70,
		MaxConcurrency:  2,
		TimeoutSeconds:  900,
		MaxWaitSeconds:  3600,
		RetryPolicy:     task.RetryPolicy{MaxRetries: 2, BackoffSeconds: 30},
		Idempotent:      true,
		Enabled:         true,
		CreatedBy:       userID,
	})
	if err != nil {
		t.Fatalf("创建任务定义：%v", err)
	}

	run, err := service.Trigger(ctx, definition.ID, task.Trigger{Type: task.TriggerManual, RequestedBy: userID})
	if err != nil {
		t.Fatalf("手动执行任务：%v", err)
	}
	_ = insertTaskVersion(t, db, scriptID, userID, 2)

	var storedVersionID string
	var state string
	var priority, cpu, timeout, maxWait, maxRetries, backoff int
	var memory, disk int64
	var idempotent bool
	err = db.QueryRow(ctx, `
		SELECT script_version_id, state, priority, cpu_millicores, memory_bytes,
		       disk_bytes, timeout_seconds, max_wait_seconds, max_retries,
		       retry_backoff_seconds, idempotent
		FROM task_runs
		WHERE id = $1
	`, run.ID).Scan(
		&storedVersionID, &state, &priority, &cpu, &memory, &disk, &timeout,
		&maxWait, &maxRetries, &backoff, &idempotent,
	)
	if err != nil {
		t.Fatalf("读取运行实例：%v", err)
	}
	if run.ScriptVersionID != versionOneID || storedVersionID != versionOneID {
		t.Fatalf("运行实例必须永久锁定创建时的版本：返回=%s 数据库=%s 期望=%s", run.ScriptVersionID, storedVersionID, versionOneID)
	}
	if state != "queued" || priority != 70 || cpu != 250 || memory != 256<<20 || disk != 512<<20 || timeout != 900 || maxWait != 3600 || maxRetries != 2 || backoff != 30 || !idempotent {
		t.Fatalf("运行实例未完整复制调度快照：state=%s priority=%d cpu=%d memory=%d disk=%d timeout=%d maxWait=%d retries=%d backoff=%d idempotent=%v", state, priority, cpu, memory, disk, timeout, maxWait, maxRetries, backoff, idempotent)
	}
	var eventType, eventState string
	if err := db.QueryRow(ctx, `SELECT event_type, state FROM run_events WHERE task_run_id = $1 AND sequence = 0`, run.ID).Scan(&eventType, &eventState); err != nil {
		t.Fatalf("读取排队事件：%v", err)
	}
	if eventType != "run.queued" || eventState != "queued" {
		t.Fatalf("应写入 run.queued 排队事件，实际 type=%s state=%s", eventType, eventState)
	}
}

func TestDisablingDefinitionCanCancelOnlyQueuedRunsWhenRequested(t *testing.T) {
	db := taskDatabase(t)
	ctx := context.Background()
	userID := insertTaskUser(t, db)
	scriptID := insertTaskScript(t, db, userID)
	_ = insertTaskVersion(t, db, scriptID, userID, 1)
	service := task.NewService(db, taskClock)
	definition, err := service.Create(ctx, validTaskInput(scriptID, userID, "可停用任务"))
	if err != nil {
		t.Fatalf("创建任务定义：%v", err)
	}
	queued, err := service.Trigger(ctx, definition.ID, task.Trigger{Type: task.TriggerManual, RequestedBy: userID})
	if err != nil {
		t.Fatalf("创建排队实例：%v", err)
	}
	running, err := service.Trigger(ctx, definition.ID, task.Trigger{Type: task.TriggerManual, RequestedBy: userID})
	if err != nil {
		t.Fatalf("创建运行实例：%v", err)
	}
	if _, err := db.Exec(ctx, `UPDATE task_runs SET state = 'running' WHERE id = $1`, running.ID); err != nil {
		t.Fatalf("准备运行中实例：%v", err)
	}

	if err := service.SetEnabled(ctx, definition.ID, false, true); err != nil {
		t.Fatalf("停用任务定义：%v", err)
	}

	var queuedState, runningState string
	if err := db.QueryRow(ctx, `SELECT state FROM task_runs WHERE id = $1`, queued.ID).Scan(&queuedState); err != nil {
		t.Fatalf("读取排队实例：%v", err)
	}
	if err := db.QueryRow(ctx, `SELECT state FROM task_runs WHERE id = $1`, running.ID).Scan(&runningState); err != nil {
		t.Fatalf("读取运行实例：%v", err)
	}
	if queuedState != "cancelled" || runningState != "running" {
		t.Fatalf("停用时只应取消排队实例：queued=%s running=%s", queuedState, runningState)
	}
}

func TestCreatePreservesZeroPriorityAndImmediateRetryPolicy(t *testing.T) {
	db := taskDatabase(t)
	ctx := context.Background()
	userID := insertTaskUser(t, db)
	scriptID := insertTaskScript(t, db, userID)
	input := validTaskInput(scriptID, userID, "最低优先级任务")
	input.Priority = 0
	input.RetryPolicy.BackoffSeconds = 0
	service := task.NewService(db, taskClock)

	definition, err := service.Create(ctx, input)
	if err != nil {
		t.Fatalf("创建最低优先级任务：%v", err)
	}
	if definition.Priority != 0 || definition.RetryPolicy.BackoffSeconds != 0 {
		t.Fatalf("显式零值不能被默认值覆盖：priority=%d backoff=%d", definition.Priority, definition.RetryPolicy.BackoffSeconds)
	}
}

func TestCreateRejectsPinnedVersionFromAnotherScript(t *testing.T) {
	db := taskDatabase(t)
	ctx := context.Background()
	userID := insertTaskUser(t, db)
	scriptID := insertTaskScript(t, db, userID)
	otherScriptID := insertTaskScript(t, db, userID)
	foreignVersionID := insertTaskVersion(t, db, otherScriptID, userID, 1)
	input := validTaskInput(scriptID, userID, "错误固定版本任务")
	input.VersionPolicy = task.VersionPinned
	input.PinnedVersionID = foreignVersionID

	_, err := task.NewService(db, taskClock).Create(ctx, input)
	if !errors.Is(err, task.ErrInvalidDefinition) {
		t.Fatalf("固定版本必须属于所选脚本，实际错误：%v", err)
	}
}

func taskDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()
	db := testpostgres.Start(t)
	testpostgres.ApplyInitialMigration(t, db)
	testpostgres.ApplyMigration(t, db, "000005_task_scheduling.up.sql")
	return db
}

func validTaskInput(scriptID, userID, name string) task.CreateInput {
	return task.CreateInput{
		Name:            name,
		ScriptID:        scriptID,
		VersionPolicy:   task.VersionLatest,
		RequiredRuntime: "bash",
		Resources:       task.Resources{CPUMillicores: 100, MemoryBytes: 128 << 20, DiskBytes: 128 << 20},
		Priority:        50,
		MaxConcurrency:  1,
		TimeoutSeconds:  3600,
		MaxWaitSeconds:  86400,
		Enabled:         true,
		CreatedBy:       userID,
	}
}

func taskClock() time.Time {
	return time.Date(2026, 8, 27, 17, 59, 0, 0, time.UTC)
}

func insertTaskUser(t *testing.T, db *pgxpool.Pool) string {
	t.Helper()
	var id string
	if err := db.QueryRow(context.Background(), `
		INSERT INTO users (email, display_name, password_hash)
		VALUES ($1, '任务管理员', 'test-hash')
		RETURNING id
	`, fmt.Sprintf("task-%d@example.com", time.Now().UnixNano())).Scan(&id); err != nil {
		t.Fatalf("写入测试用户：%v", err)
	}
	return id
}

func insertTaskScript(t *testing.T, db *pgxpool.Pool, userID string) string {
	t.Helper()
	var id string
	if err := db.QueryRow(context.Background(), `
		INSERT INTO scripts (name, description, runtime, created_by)
		VALUES ($1, '测试任务脚本', 'bash', $2)
		RETURNING id
	`, fmt.Sprintf("任务脚本-%d", time.Now().UnixNano()), userID).Scan(&id); err != nil {
		t.Fatalf("写入测试脚本：%v", err)
	}
	return id
}

func insertTaskVersion(t *testing.T, db *pgxpool.Pool, scriptID, userID string, number int) string {
	t.Helper()
	var id string
	if err := db.QueryRow(context.Background(), `
		INSERT INTO script_versions (
			script_id, version, artifact_uri, artifact_sha256, entrypoint,
			manifest, release_notes, created_by
		)
		VALUES ($1, $2, $3, repeat('a', 64), 'main.sh', '{"runtime":"bash"}', '测试版本', $4)
		RETURNING id
	`, scriptID, number, fmt.Sprintf("scripts/%s/v%d.tar.gz", scriptID, number), userID).Scan(&id); err != nil {
		t.Fatalf("写入脚本版本：%v", err)
	}
	return id
}
