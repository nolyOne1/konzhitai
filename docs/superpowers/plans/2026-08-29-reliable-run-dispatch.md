# 云令可靠任务派发实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把已经写入 `assigned` 的运行实例可靠下发到目标代理，并让现有京东云验收实例完成真实执行。

**Architecture:** API 内新增持久化派发器，从 PostgreSQL 按重试窗口原子领取运行实例，构造现有 `agentprotocol.ExecutionCommand` 并通过 `AgentConnectionHub` 发送。中央端提供至少一次投递，代理按运行 ID 与执行令牌幂等拒绝重复启动；状态仍由代理事件服务推进。

**Tech Stack:** Go 1.27、PostgreSQL 18、coder/websocket、Docker Compose、systemd、Go testing、testcontainers-go。

**Spec:** `docs/superpowers/specs/2026-08-29-reliable-run-dispatch-design.md`

## Global Constraints

- 所有用户可见文案、运行错误和运维记录使用中文。
- 不引入新长期服务或新第三方依赖；继续使用 PostgreSQL 与现有代理 WebSocket。
- 派发语义是同一执行令牌下的幂等至少一次投递，不生成新令牌。
- 敏感参数不得写入日志、数据库派发错误、命令行或验收输出。
- 生产部署先更新向后兼容的代理，再迁移数据库并滚动更新 API。
- 不创建第二个验收运行实例；部署后续跑现有运行 ID `31109d87-f742-41b0-b198-9c45de7dc287`。

---

### Task 1: 派发迁移与 PostgreSQL 原子领取

**Files:**
- Create: `migrations/000009_run_dispatch.up.sql`
- Create: `migrations/000009_run_dispatch.down.sql`
- Create: `internal/dispatch/model.go`
- Create: `internal/dispatch/postgres.go`
- Create: `internal/dispatch/postgres_test.go`

**Interfaces:**
- Produces: `dispatch.Run`, `dispatch.Store`, `dispatch.NewPostgresStore(*pgxpool.Pool) *PostgresStore`
- Produces: `Claim(context.Context, time.Time, time.Time, int) ([]dispatch.Run, error)` and `RecordResult(context.Context, string, string, string) error`
- Consumes: existing `task_runs`, `task_definitions`, `script_versions` and `scripts` tables.

- [ ] **Step 1: Write the failing migration/store tests**

Create `internal/dispatch/postgres_test.go` with a real `testpostgres.Start(t)` database. Apply migrations `000001` through `000009`, seed one `assigned` run and one recently attempted run, then assert only the due row is claimed:

```go
func TestPostgresStoreClaimsOnlyDueAssignedRuns(t *testing.T) {
    db := dispatchDatabase(t)
    now := time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)
    dueID, recentID := seedDispatchRuns(t, db, now)
    store := dispatch.NewPostgresStore(db)

    runs, err := store.Claim(context.Background(), now.Add(-10*time.Second), now, 20)
    if err != nil {
        t.Fatalf("领取待派发运行：%v", err)
    }
    if len(runs) != 1 || runs[0].ID != dueID {
        t.Fatalf("只能领取到期运行：%+v；最近运行=%s", runs, recentID)
    }
    if runs[0].ExecutionToken != "token-due" || runs[0].ScriptID == "" || runs[0].Entrypoint != "main.sh" {
        t.Fatalf("执行载荷不完整：%+v", runs[0])
    }
}
```

Add a concurrent claim test using two goroutines and assert the same run is returned exactly once. Add a `RecordResult` test asserting a transient error is saved only while state and token still match.

- [ ] **Step 2: Run tests to verify RED**

Run: `go test ./internal/dispatch -run 'TestPostgresStore' -count=1`

Expected: FAIL because `internal/dispatch` and migration `000009` do not exist.

- [ ] **Step 3: Add the forward/backward migration**

Create `migrations/000009_run_dispatch.up.sql`:

```sql
ALTER TABLE task_runs
    ADD COLUMN dispatch_attempts integer NOT NULL DEFAULT 0 CHECK (dispatch_attempts >= 0),
    ADD COLUMN last_dispatch_at timestamptz,
    ADD COLUMN dispatch_error text NOT NULL DEFAULT '';

CREATE INDEX task_runs_dispatch_due_idx
    ON task_runs (last_dispatch_at, assigned_at)
    WHERE state = 'assigned';
```

Create `migrations/000009_run_dispatch.down.sql`:

```sql
DROP INDEX IF EXISTS task_runs_dispatch_due_idx;
ALTER TABLE task_runs
    DROP COLUMN IF EXISTS dispatch_error,
    DROP COLUMN IF EXISTS last_dispatch_at,
    DROP COLUMN IF EXISTS dispatch_attempts;
```

- [ ] **Step 4: Implement the store model and atomic CTE claim**

Define the payload in `internal/dispatch/model.go`:

```go
type Run struct {
    ID, ExecutionToken, ServerID string
    ScriptID, ScriptVersionID, Runtime, Entrypoint string
    Parameters map[string]any
    SecretBindings map[string]string
    Resources agentprotocol.ResourceLimits
    Timeout time.Duration
    Attempt int
}

type Store interface {
    Claim(ctx context.Context, cutoff, now time.Time, limit int) ([]Run, error)
    RecordResult(ctx context.Context, runID, executionToken, dispatchError string) error
}
```

Implement `Claim` with one transaction and a `WITH candidates ... FOR UPDATE SKIP LOCKED` CTE. It must update `dispatch_attempts`, `last_dispatch_at`, `dispatch_error=''`, then join the claimed rows to definitions and versions. Decode both JSON columns and convert `timeout_seconds` to `time.Duration`. `RecordResult` must cap errors at 1000 UTF-8 characters and update only `WHERE id=$1 AND execution_token=$2 AND state='assigned'`.

- [ ] **Step 5: Run store and migration tests to verify GREEN**

Run: `go test ./internal/dispatch -run 'TestPostgresStore' -count=1`

Expected: PASS; concurrent test reports exactly one claim.

- [ ] **Step 6: Commit Task 1**

```bash
git add migrations/000009_run_dispatch.up.sql migrations/000009_run_dispatch.down.sql internal/dispatch/model.go internal/dispatch/postgres.go internal/dispatch/postgres_test.go
git commit -m "feat: 增加持久化任务派发领取"
```

---

### Task 2: 构造安全执行载荷并下发

**Files:**
- Create: `internal/dispatch/service.go`
- Create: `internal/dispatch/service_test.go`

**Interfaces:**
- Consumes: `dispatch.Store` from Task 1.
- Consumes: `SendExecutionCommand(context.Context, string, agentprotocol.ExecutionCommand) error` from the existing connection hub.
- Consumes: `ResolveForRun(context.Context, []secret.ID) (map[string]string, error)` from `secret.Service`.
- Consumes: `Apply(context.Context, agentprotocol.RunEvent) error` from `task.EventService`.
- Produces: `dispatch.NewService(...) *dispatch.Service` and `(*Service).Dispatch(context.Context) error`.

- [ ] **Step 1: Write failing service tests for success, retry and permanent failure**

Use in-memory fakes, but assert the real `agentprotocol.ExecutionCommand` passed to the sender:

```go
func TestServiceDispatchesCompleteAssignment(t *testing.T) {
    store := &fakeStore{runs: []dispatch.Run{{
        ID: "run-1", ExecutionToken: "token-1", ServerID: "server-1",
        ScriptID: "script-1", ScriptVersionID: "version-1",
        Runtime: "bash", Entrypoint: "main.sh",
        Parameters: map[string]any{"日期": "2026-08-29"},
        SecretBindings: map[string]string{"访问令牌": "secret-1"},
        Resources: agentprotocol.ResourceLimits{CPUMillicores: 100, MemoryBytes: 64 << 20, DiskBytes: 16 << 20},
        Timeout: time.Minute,
    }}}
    sender := &fakeSender{}
    resolver := &fakeResolver{values: map[string]string{"secret-1": "不可输出的值"}}
    service := dispatch.NewService(store, sender, resolver, &fakeFailureSink{}, fixedNow)

    if err := service.Dispatch(context.Background()); err != nil {
        t.Fatalf("派发运行：%v", err)
    }
    assignment := sender.command.Assignment
    if assignment == nil || assignment.RunID != "run-1" || assignment.ExecutionToken != "token-1" {
        t.Fatalf("执行身份不完整：%+v", sender.command)
    }
    if assignment.ScriptPath != "/var/lib/yunling-agent/script-cache/scripts/script-1/version-1/main.sh" {
        t.Fatalf("脚本路径错误：%s", assignment.ScriptPath)
    }
    if assignment.Environment["YUNLING_PARAMETERS_JSON"] != `{"日期":"2026-08-29"}` {
        t.Fatalf("普通参数未下发：%+v", assignment.Environment)
    }
    if assignment.Environment["YUNLING_SECRETS_JSON"] != `{"访问令牌":"不可输出的值"}` {
        t.Fatal("敏感参数绑定未按名称下发")
    }
}
```

Add `TestServiceKeepsAssignedRunForConnectionFailure`: sender returns an error; assert `RecordResult` receives a non-empty sanitized message and failure sink is not called. Add `TestServiceFailsRunWhenSecretCannotResolve`: resolver fails; assert the failure sink receives a sequence-1 `failed` event whose message does not contain a secret ID or value.

- [ ] **Step 2: Run tests to verify RED**

Run: `go test ./internal/dispatch -run 'TestService' -count=1`

Expected: FAIL because `Service` does not exist.

- [ ] **Step 3: Implement assignment building and dispatch policy**

In `service.go`, define narrow interfaces and constants:

```go
const (
    DefaultBatchSize = 20
    DefaultRetryInterval = 10 * time.Second
    DefaultScanInterval = 2 * time.Second
    DefaultTasksMax = 64
)

type CommandSender interface {
    SendExecutionCommand(context.Context, string, agentprotocol.ExecutionCommand) error
}
type SecretResolver interface {
    ResolveForRun(context.Context, []secret.ID) (map[string]string, error)
}
type FailureSink interface {
    Apply(context.Context, agentprotocol.RunEvent) error
}
```

`Dispatch` calls `Claim(now-DefaultRetryInterval, now, DefaultBatchSize)`. Build the script path with `path.Join` only after validating `Entrypoint == path.Clean(Entrypoint)`, it is relative, and it does not begin with `../`. JSON-marshal ordinary parameters and a name-to-value secret map. Use fixed environment keys `YUNLING_RUN_ID`, `YUNLING_SCRIPT_VERSION_ID`, `YUNLING_PARAMETERS_JSON`, `YUNLING_SECRETS_JSON`; keep `Arguments` empty.

Treat every sender error as transient: call `RecordResult` and continue with other runs. Treat invalid payload or secret resolution as permanent: apply a sequence-1 `failed` event with exit code `-1` and a generic Chinese message, without embedding the resolver error. Return `errors.Join` only for store/failure-recording infrastructure errors.

- [ ] **Step 4: Run service tests and package tests**

Run: `go test ./internal/dispatch -count=1`

Expected: PASS.

- [ ] **Step 5: Commit Task 2**

```bash
git add internal/dispatch/service.go internal/dispatch/service_test.go
git commit -m "feat: 下发完整任务执行载荷"
```

---

### Task 3: 代理同令牌幂等

**Files:**
- Modify: `internal/executor/runner.go`
- Modify: `internal/executor/runner_test.go`
- Modify: `internal/agent/execution_client.go`
- Modify: `internal/agent/execution_client_test.go`

**Interfaces:**
- Consumes: existing `executor.ErrRunAlreadyActive` and `executor.ErrExecutionTokenMismatch`.
- Produces: same-token duplicate assignments return `ErrRunAlreadyActive`; `ExecutionClient` treats only that sentinel as a no-op.

- [ ] **Step 1: Write failing Runner idempotency tests**

Add to `runner_test.go`:

```go
func TestRunnerDistinguishesDuplicateAndConflictingAssignment(t *testing.T) {
    launcher := newFakeLauncher(true)
    runner, assignment := newTestRunner(t, launcher, time.Second)
    if _, err := runner.Start(context.Background(), assignment); err != nil {
        t.Fatal(err)
    }
    if _, err := runner.Start(context.Background(), assignment); !errors.Is(err, executor.ErrRunAlreadyActive) {
        t.Fatalf("同令牌重复派发必须幂等拒绝：%v", err)
    }
    conflicting := assignment
    conflicting.ExecutionToken = "token-2"
    if _, err := runner.Start(context.Background(), conflicting); !errors.Is(err, executor.ErrExecutionTokenMismatch) {
        t.Fatalf("不同令牌重复派发必须拒绝：%v", err)
    }
    if launcher.starts != 1 {
        t.Fatalf("重复派发不得启动第二个进程：%d", launcher.starts)
    }
}
```

Increment `fakeLauncher.starts` in its `Start` method.

- [ ] **Step 2: Write failing ExecutionClient duplicate test**

Configure a runner that returns `executor.ErrRunAlreadyActive` for the first command, then succeeds for a second different run. Assert no failed event is sent for the duplicate and the client continues to receive the second command.

- [ ] **Step 3: Run focused tests to verify RED**

Run: `go test ./internal/executor ./internal/agent -run 'Duplicate|Idempot|Conflicting' -count=1`

Expected: Runner cannot distinguish tokens and ExecutionClient currently emits a failed event.

- [ ] **Step 4: Implement the minimal token-aware checks**

In `Runner.Start`, inspect `r.active[assignment.RunID]` before preparing the working directory and again immediately before launcher start. Return `ErrRunAlreadyActive` only when the active token equals the incoming token; otherwise return `ErrExecutionTokenMismatch`.

In `ExecutionClient.Run`, handle the idempotent sentinel before creating a failed event:

```go
events, err := c.runner.Start(ctx, *command.Assignment)
if errors.Is(err, executor.ErrRunAlreadyActive) {
    continue
}
if err != nil {
    // existing sequence-1 failed event path
}
```

- [ ] **Step 5: Run complete executor and agent tests**

Run: `go test ./internal/executor ./internal/agent -count=1`

Expected: PASS.

- [ ] **Step 6: Commit Task 3**

```bash
git add internal/executor/runner.go internal/executor/runner_test.go internal/agent/execution_client.go internal/agent/execution_client_test.go
git commit -m "fix: 保证代理重复派发幂等"
```

---

### Task 4: API 接线与可部署代理构建

**Files:**
- Modify: `cmd/api/main.go`
- Modify: `deploy/Dockerfile.services`
- Create: `internal/dispatch/loop_test.go`

**Interfaces:**
- Consumes: `dispatch.NewPostgresStore`, `dispatch.NewService`, `dispatch.RunLoop` from Tasks 1-2.
- Consumes: concrete `*secret.Service` when the master key is available; a nil resolver remains valid only for tasks without secret bindings.
- Produces: API 后台每 2 秒派发一次；services 镜像包含 `/usr/local/bin/yunling-agent` 供安全提取部署。

- [ ] **Step 1: Write the failing loop lifecycle test**

Create `internal/dispatch/loop_test.go` with a fake dispatcher and a 5 ms interval. Assert the loop dispatches on start, dispatches again after an interval, logs/continues after one error, and exits when context is cancelled. The production signature must be:

```go
func RunLoop(ctx context.Context, service interface{ Dispatch(context.Context) error }, interval time.Duration, logError func(error))
```

- [ ] **Step 2: Run loop test to verify RED**

Run: `go test ./internal/dispatch -run TestRunLoop -count=1`

Expected: FAIL because `RunLoop` does not exist.

- [ ] **Step 3: Implement loop and wire API**

Implement an immediate first dispatch followed by a `time.NewTicker(DefaultScanInterval)` loop. In `cmd/api/main.go`, retain a concrete `var dispatchSecretResolver *secret.Service`, assign it beside `secretManager`, then create:

```go
dispatchService := dispatch.NewService(
    dispatch.NewPostgresStore(pool),
    connections,
    dispatchSecretResolver,
    eventService,
    time.Now,
)
go dispatch.RunLoop(context.Background(), dispatchService, dispatch.DefaultScanInterval, func(err error) {
    log.Printf("任务派发扫描失败：%v", err)
})
```

Do not log assignment environment or resolved secret values.

- [ ] **Step 4: Make the services image export the agent binary**

Extend the existing builder command in `deploy/Dockerfile.services`:

```dockerfile
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/yunling-api ./cmd/api && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/yunling-scheduler ./cmd/scheduler && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/yunling-agent ./cmd/agent && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/yunling-bootstrap ./cmd/bootstrap
```

Copy `/out/yunling-agent` into the final image at `/usr/local/bin/yunling-agent`; it is not the entrypoint and does not change existing service behavior.

- [ ] **Step 5: Run Go, migration and Compose verification**

Run:

```bash
go test ./...
docker compose --env-file deploy/.env.example -f deploy/docker-compose.yml config --quiet
docker build -f deploy/Dockerfile.services -t yunling-services:dispatch-test .
```

Expected: all Go tests pass, Compose config exits 0, and the image builds all four binaries.

- [ ] **Step 6: Commit Task 4**

```bash
git add cmd/api/main.go deploy/Dockerfile.services internal/dispatch/loop_test.go internal/dispatch/service.go
git commit -m "feat: 启动可靠任务派发器"
```

---

### Task 5: 全量回归与生产部署

**Files:**
- Modify: `deploy/PRODUCTION.md`

**Interfaces:**
- Consumes: all artifacts from Tasks 1-4.
- Produces: production migration, new API image, new JD agent binary, completed run and recorded evidence.

- [ ] **Step 1: Verify the local branch before deployment**

Run:

```bash
git diff --check
go test ./...
npm --workspace apps/web test -- --run
npm --workspace apps/web run build
git status --short
```

Expected: no whitespace errors; all Go tests, 20 web tests and web build pass; only intended tracked files appear.

- [ ] **Step 2: Back up the production database and source state**

On Tencent Cloud, create a root-only PostgreSQL dump under `/opt/backups/yunling-before-dispatch-20260829.sql`, record its SHA-256, and verify mode `0600`. Do not include the admin password or master key in the backup command output.

- [ ] **Step 3: Transfer the committed source and build once**

Synchronize only the committed repository contents to `/opt/yunling`, preserving `deploy/.env`, `deploy/secrets`, uploads and backups. Build the `api` image with the existing Compose file. Confirm `/usr/local/bin/yunling-api` and `/usr/local/bin/yunling-agent` both exist in the built image.

- [ ] **Step 4: Deploy the backward-compatible JD agent first**

Extract `/usr/local/bin/yunling-agent` from the built image to a root-owned staging file, calculate SHA-256, transfer it to JD Cloud, install as `/usr/local/bin/yunling-agent` mode `0755`, and restart `yunling-agent.service`. Verify:

```bash
systemctl is-active yunling-agent.service
systemctl is-enabled yunling-agent.service
systemctl show yunling-agent.service -p NRestarts -p MainPID
journalctl -u yunling-agent.service --since '-5 min' --no-pager
```

Expected: active, enabled, a new PID, no restart loop, and a successful connection for server `6f445eb6-e388-4ebf-a67d-33834c816893`.

- [ ] **Step 5: Apply migration 000009 and roll API**

Execute `/migrations/000009_run_dispatch.up.sql` inside the PostgreSQL container with `ON_ERROR_STOP=1`. Query `information_schema.columns` to verify the three new columns, then run `docker compose up -d --no-deps api`. Confirm the API healthcheck becomes healthy without restarting PostgreSQL, Redis, MinIO, Scheduler, Web or Caddy.

- [ ] **Step 6: Verify the existing run completes without a duplicate**

Poll the existing run ID `31109d87-f742-41b0-b198-9c45de7dc287`. Assert the database still contains exactly one task run for `京东云只读诊断验收任务`, and wait for `succeeded` with exit code `0` and assigned server `6f445eb6-e388-4ebf-a67d-33834c816893`.

Read `/api/runs/31109d87-f742-41b0-b198-9c45de7dc287/events?follow=false` with an in-memory authenticated session. The stdout log must contain:

```text
云令真实节点验收
主机名=
时间=
CPU核数=
可用内存KB=
根分区可用KB=
```

- [ ] **Step 7: Record evidence and run final health checks**

Update `deploy/PRODUCTION.md` with deployment commit, migration number, API/agent hashes, run ID, final state, exit code and server ID. Do not copy passwords, session cookies, execution tokens or sensitive parameter values.

Run final checks:

```bash
docker compose --env-file deploy/.env -f deploy/docker-compose.yml ps
curl --fail --show-error https://aiwise.top/api/health
systemctl status yunling-agent.service --no-pager
git diff --check
git status --short --branch
```

- [ ] **Step 8: Commit production evidence**

```bash
git add deploy/PRODUCTION.md
git commit -m "docs: 记录可靠任务派发上线验收"
```
