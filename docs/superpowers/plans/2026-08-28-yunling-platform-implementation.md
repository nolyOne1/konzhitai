# 云令脚本调度平台 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 构建一个可部署在腾讯云、通过轻量代理管理多台服务器脚本并按资源自动调度任务的全中文平台。

**Architecture:** 采用 React 控制台、Go 控制 API、Go 调度器和 Go 执行代理组成的单仓库。PostgreSQL 保存最终业务状态，Redis 保存等待队列和资源租约，S3 兼容存储保存不可变脚本包与归档日志；执行服务器只建立出站 TLS 连接。

**Tech Stack:** Go 1.27、Node.js 24 LTS、React、TypeScript、Vite、TanStack Query、React Router、Vitest、Playwright、PostgreSQL、Redis、MinIO、Docker Compose、systemd

**Spec:** `docs/superpowers/specs/2026-08-28-yunling-script-orchestration-design.md`

## Global Constraints

- 所有用户可见文字、错误提示、状态名称和文档使用中文。
- 管理系统部署在腾讯云；执行服务器可以来自任意云厂商。
- 代理主动连接中央服务，SSH 只用于首次安装和紧急诊断。
- 不保存现有 root 密码；每台服务器使用独立代理身份。
- 脚本版本不可变，运行实例在创建时锁定确定版本。
- 无合适服务器时执行实例保持“排队中”，资源变化后自动重新调度。
- 调度必须同时检查在线状态、标签、运行环境、并发、CPU、内存、磁盘和隔离状态。
- 参数不得直接拼接为 shell 命令；敏感参数必须加密并在日志中脱敏。
- 每个任务遵循测试先行：先看到目标测试失败，再写最小实现，再运行相关测试和全量测试。
- 每个任务单独提交，不把后续任务的代码提前混入当前提交。

---

## 1. 文件与模块边界

```text
.
├── apps/web/                         # React 全中文控制台
│   ├── src/app/                      # 路由、布局、全局 Provider
│   ├── src/api/                      # OpenAPI 类型客户端与 SSE 客户端
│   ├── src/features/auth/            # 登录与会话
│   ├── src/features/dashboard/       # 运行总览
│   ├── src/features/servers/         # 服务器管理与资源状态
│   ├── src/features/scripts/         # 脚本、版本、同步与回滚
│   ├── src/features/tasks/           # 任务定义与定时计划
│   ├── src/features/runs/            # 执行记录、日志、取消与重跑
│   ├── src/features/settings/        # 成员、权限、密钥与审计
│   └── e2e/                          # Playwright 端到端测试
├── api/openapi.yaml                  # 控制台 HTTP/SSE 接口契约
├── cmd/api/main.go                   # 中央 API 进程入口
├── cmd/scheduler/main.go             # 调度器进程入口
├── cmd/agent/main.go                 # 执行代理入口
├── internal/auth/                    # 登录、会话和 RBAC
├── internal/server/                  # 服务器、代理身份和心跳
├── internal/agent/                   # 代理配置、资源采集和中央连接
├── internal/agentprotocol/           # 代理 WebSocket 消息协议
├── internal/script/                  # 脚本草稿、版本和发布
├── internal/artifact/                # S3 兼容对象存储接口
├── internal/task/                    # 任务定义、计划和执行状态机
├── internal/scheduler/               # 队列、候选筛选、评分和资源租约
├── internal/executor/                # 代理侧下载、隔离执行和终止
├── internal/logstream/               # 日志分块、续传、归档和 SSE
├── internal/secret/                  # 敏感参数加密与脱敏
├── internal/audit/                   # 审计事件
├── internal/store/postgres/          # PostgreSQL 连接和仓储实现
├── internal/store/redis/             # Redis 队列和租约实现
├── migrations/                       # 版本化数据库迁移
├── deploy/docker-compose.yml         # 腾讯云控制中心部署
├── deploy/agent/yunling-agent.service# 执行节点 systemd 单元
├── deploy/agent/install.sh            # 代理安装脚本
└── tests/integration/                 # 跨组件集成测试
```

边界规则：`internal/*` 业务包不导入 `cmd/*`；Web 只通过 `api/openapi.yaml` 暴露的接口访问后端；代理只依赖 `agentprotocol`、`executor` 和最小配置包；调度器通过仓储接口访问 PostgreSQL 与 Redis。

---

### Task 1: 建立可运行的单仓库骨架

**Files:**
- Create: `go.mod`
- Create: `package.json`
- Create: `apps/web/package.json`
- Create: `apps/web/vite.config.ts`
- Create: `apps/web/src/app/App.tsx`
- Create: `apps/web/src/app/styles.css`
- Create: `apps/web/src/app/App.test.tsx`
- Create: `api/openapi.yaml`
- Create: `cmd/api/main.go`
- Create: `internal/health/handler.go`
- Create: `internal/health/handler_test.go`
- Create: `.gitignore`
- Create: `Makefile`

**Interfaces:**
- Produces: `health.Handler() http.Handler`。
- Produces: `GET /api/health` 返回 `{"status":"ok"}`。
- Produces: Web 根布局显示“云令”和“脚本调度中心”。

- [x] **Step 1: 先写 API 健康检查失败测试**

```go
func TestHandlerReturnsOK(t *testing.T) {
    req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
    rec := httptest.NewRecorder()
    health.Handler().ServeHTTP(rec, req)
    require.Equal(t, http.StatusOK, rec.Code)
    require.JSONEq(t, `{"status":"ok"}`, rec.Body.String())
}
```

- [x] **Step 2: 运行测试并确认失败**

Run: `go test ./internal/health -run TestHandlerReturnsOK -v`  
Expected: FAIL，提示 `internal/health` 或 `health.Handler` 不存在。

- [x] **Step 3: 写最小 API 实现和入口**

`go.mod` 使用 `module yunling.local/platform` 和 `go 1.27.0`。运行 `go get github.com/stretchr/testify@latest` 安装测试断言库，再运行 `go mod tidy` 生成锁定依赖的 `go.sum`。

```go
func Handler() http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
        w.Header().Set("Content-Type", "application/json; charset=utf-8")
        _, _ = io.WriteString(w, `{"status":"ok"}`)
    })
}
```

`cmd/api/main.go` 将该处理器挂载到 `/api/health`，监听地址从 `YUNLING_HTTP_ADDR` 读取，默认 `:8080`。

- [x] **Step 4: 先写中文应用壳失败测试**

```tsx
it('显示云令中文控制台框架', () => {
  render(<App />)
  expect(screen.getByText('云令')).toBeInTheDocument()
  expect(screen.getByText('脚本调度中心')).toBeInTheDocument()
  expect(screen.getByRole('navigation', { name: '主导航' })).toBeVisible()
})
```

- [x] **Step 5: 初始化 React/Vite 并实现最小应用壳**

根 `package.json` 声明 `apps/web` workspace。运行 `npm install --workspace apps/web react react-dom react-router-dom @tanstack/react-query`，再运行 `npm install --workspace apps/web --save-dev vite typescript @vitejs/plugin-react vitest jsdom @testing-library/react @testing-library/jest-dom @testing-library/user-event`，生成并提交 `package-lock.json`。

`App.tsx` 使用语义化侧栏和主内容区，主导航先包含：运行总览、脚本中心、任务调度、执行记录、服务器、脚本同步、参数与密钥、团队与权限、系统设置。`styles.css` 定义已确认的深绿色侧栏、浅色数据区、中文字体栈、44px 触控目标、清晰焦点和 560/820px 响应式断点。

- [x] **Step 6: 运行基础测试和构建**

Run: `go test ./internal/health -v`  
Expected: PASS。

Run: `npm --workspace apps/web test -- --run`  
Expected: PASS。

Run: `npm --workspace apps/web run build`  
Expected: PASS，并生成 `apps/web/dist`。

- [x] **Step 7: 提交骨架**

```bash
git add go.mod go.sum package.json package-lock.json apps/web api/openapi.yaml cmd/api internal/health .gitignore Makefile
git commit -m "chore: 建立云令单仓库骨架"
```

---

### Task 2: 建立领域类型、数据库迁移与仓储边界

**Files:**
- Create: `internal/server/model.go`
- Create: `internal/script/model.go`
- Create: `internal/task/model.go`
- Create: `internal/auth/model.go`
- Create: `internal/audit/model.go`
- Create: `internal/store/postgres/db.go`
- Create: `internal/store/postgres/migrations_test.go`
- Create: `internal/store/postgres/test_helpers_test.go`
- Create: `migrations/000001_initial.up.sql`
- Create: `migrations/000001_initial.down.sql`

**Interfaces:**
- Produces: `task.RunState` 常量 `queued`、`scheduling`、`assigned`、`syncing`、`running`、`succeeded`、`failed`、`timed_out`、`cancelled`、`expired`、`unknown`。
- Produces: `postgres.Open(ctx context.Context, dsn string) (*pgxpool.Pool, error)`。
- Produces: PostgreSQL 表 `users`、`roles`、`user_roles`、`servers`、`server_snapshots`、`scripts`、`script_drafts`、`script_versions`、`script_syncs`、`secrets`、`task_definitions`、`task_schedules`、`task_runs`、`resource_leases`、`run_events`、`log_chunks`、`audit_logs`。

- [x] **Step 1: 写状态机和迁移失败测试**

```go
func TestRunStateTerminal(t *testing.T) {
    require.True(t, task.Succeeded.Terminal())
    require.True(t, task.Failed.Terminal())
    require.True(t, task.Expired.Terminal())
    require.False(t, task.Queued.Terminal())
    require.False(t, task.Unknown.Terminal())
}
```

```go
func TestInitialMigrationCreatesCoreTables(t *testing.T) {
    db := startPostgres(t)
    applyMigrations(t, db)
    for _, table := range []string{"servers", "script_versions", "task_runs", "resource_leases", "audit_logs"} {
        require.True(t, tableExists(t, db, table), table)
    }
}
```

- [x] **Step 2: 运行目标测试并确认失败**

Run: `go test ./internal/task ./internal/store/postgres -run 'TestRunStateTerminal|TestInitialMigrationCreatesCoreTables' -v`  
Expected: FAIL，缺少领域类型与迁移文件。

- [x] **Step 3: 实现领域类型和初始迁移**

```go
type RunState string

const (
    Queued RunState = "queued"
    Scheduling RunState = "scheduling"
    Assigned RunState = "assigned"
    Syncing RunState = "syncing"
    Running RunState = "running"
    Succeeded RunState = "succeeded"
    Failed RunState = "failed"
    TimedOut RunState = "timed_out"
    Cancelled RunState = "cancelled"
    Expired RunState = "expired"
    Unknown RunState = "unknown"
)
```

迁移为每个实体使用 UUID 主键和 `created_at`；版本、运行事件、日志块、审计日志禁止更新历史内容；`task_runs` 对 `(task_definition_id, scheduled_for)` 建唯一索引避免定时任务重复创建。

测试运行时使用 `github.com/fergusstrange/embedded-postgres@v1.34.0` 启动工作区本地 PostgreSQL 18.3，缓存、运行时和数据目录均放在 `.tools/embedded-postgres/`。生产部署仍使用 Docker Compose 管理独立 PostgreSQL 服务。

- [x] **Step 4: 运行领域和迁移测试**

Run: `go test ./internal/task ./internal/store/postgres -v`  
Expected: PASS。

- [x] **Step 5: 提交领域与迁移**

```bash
git add internal/server internal/script internal/task internal/auth internal/audit internal/store/postgres migrations
git commit -m "feat: 建立核心领域模型和数据库迁移"
```

---

### Task 3: 实现登录、会话与四级权限

**Files:**
- Create: `internal/auth/password.go`
- Create: `internal/auth/session.go`
- Create: `internal/auth/middleware.go`
- Create: `internal/auth/service_test.go`
- Create: `apps/web/src/features/auth/LoginPage.tsx`
- Create: `apps/web/src/features/auth/LoginPage.test.tsx`
- Modify: `api/openapi.yaml`
- Modify: `cmd/api/main.go`

**Interfaces:**
- Produces: `auth.Service.Login(ctx, email, password) (Session, error)`。
- Produces: `auth.Require(permission string) func(http.Handler) http.Handler`。
- Produces: `POST /api/auth/login`、`POST /api/auth/logout`、`GET /api/auth/session`。
- Produces: 权限 `system.admin`、`operations.execute`、`scripts.publish`、`system.read`。

- [x] **Step 1: 写密码、登录和权限失败测试**

```go
func TestOperatorCannotPublishScript(t *testing.T) {
    role := auth.RoleOperator
    require.True(t, role.Allows("operations.execute"))
    require.False(t, role.Allows("scripts.publish"))
}

func TestWrongPasswordReturnsInvalidCredentials(t *testing.T) {
    svc := auth.NewService(fakeUsers{passwordHash: hashForTest(t, "正确密码")}, testSessions{})
    _, err := svc.Login(context.Background(), "ops@example.com", "错误密码")
    require.ErrorIs(t, err, auth.ErrInvalidCredentials)
}
```

- [x] **Step 2: 运行测试并确认失败**

Run: `go test ./internal/auth -v`  
Expected: FAIL，缺少角色、密码哈希和会话实现。

- [x] **Step 3: 实现 Argon2id 密码哈希、服务端会话和 RBAC**

角色映射固定为：管理员拥有全部权限；运维人员可执行、终止和管理服务器；脚本开发者可创建草稿和发布脚本；只读成员仅有 `system.read`。会话令牌只通过 `HttpOnly`、`Secure`、`SameSite=Lax` Cookie 返回，数据库只保存令牌哈希。

- [x] **Step 4: 写并实现中文登录页**

```tsx
it('登录失败时在表单旁显示中文错误', async () => {
  server.use(http.post('/api/auth/login', () => HttpResponse.json({ message: '账号或密码错误' }, { status: 401 })))
  render(<LoginPage />)
  await userEvent.type(screen.getByLabelText('邮箱'), 'ops@example.com')
  await userEvent.type(screen.getByLabelText('密码'), 'bad')
  await userEvent.click(screen.getByRole('button', { name: '登录' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('账号或密码错误')
})
```

- [x] **Step 5: 运行认证测试与 Web 测试**

Run: `go test ./internal/auth -v`  
Expected: PASS。

Run: `npm --workspace apps/web test -- --run src/features/auth/LoginPage.test.tsx`  
Expected: PASS。

- [x] **Step 6: 提交认证能力**

```bash
git add internal/auth apps/web/src/features/auth api/openapi.yaml cmd/api/main.go
git commit -m "feat: 添加中文登录和角色权限"
```

---

### Task 4: 实现代理注册、心跳和资源采集

**Files:**
- Create: `internal/agentprotocol/messages.go`
- Create: `internal/server/registry.go`
- Create: `internal/server/registry_test.go`
- Create: `internal/server/http.go`
- Create: `internal/agent/config.go`
- Create: `internal/agent/collector.go`
- Create: `internal/agent/collector_test.go`
- Create: `internal/agent/client.go`
- Create: `cmd/agent/main.go`
- Create: `migrations/000002_agent_enrollment.up.sql`
- Create: `migrations/000002_agent_enrollment.down.sql`
- Modify: `api/openapi.yaml`
- Modify: `cmd/api/main.go`

**Interfaces:**
- Produces: `agentprotocol.Heartbeat{ServerID, Sequence, SentAt, CPUTotalMilli, CPUUsedMilli, MemoryTotalBytes, MemoryUsedBytes, DiskTotalBytes, DiskFreeBytes, RunningTasks, Runtimes, AgentVersion}`。
- Produces: `server.Registry.AcceptHeartbeat(ctx, heartbeat) error`；小于等于已保存序号的心跳被忽略。
- Produces: `POST /api/servers/enrollment-tokens`、`POST /api/agent/enroll` 和 `GET /api/agent/connect` WebSocket 升级端点。
- Produces: `agent.Collector.Snapshot(ctx) (agentprotocol.Heartbeat, error)`。

- [x] **Step 1: 写乱序心跳和资源采集失败测试**

```go
func TestRegistryIgnoresOlderHeartbeat(t *testing.T) {
    repo := newMemoryServerRepo()
    registry := server.NewRegistry(repo, fixedClock())
    require.NoError(t, registry.AcceptHeartbeat(ctx, heartbeat(10, 3200)))
    require.NoError(t, registry.AcceptHeartbeat(ctx, heartbeat(9, 900)))
    require.Equal(t, int64(3200), repo.snapshot.CPUUsedMilli)
}
```

```go
func TestCollectorReportsConfiguredRuntimes(t *testing.T) {
    c := agent.NewCollector(fakeStats{cpu: 2500, memory: 4 << 30}, []string{"bash", "python3"})
    got, err := c.Snapshot(context.Background())
    require.NoError(t, err)
    require.Equal(t, []string{"bash", "python3"}, got.Runtimes)
}
```

- [x] **Step 2: 运行测试并确认失败**

Run: `go test ./internal/server ./internal/agent -v`  
Expected: FAIL，缺少协议、Registry 和 Collector。

- [x] **Step 3: 实现一次性注册令牌和代理独立身份**

注册令牌只保存哈希、有效期和是否使用；首次注册后返回服务器 ID 与代理凭据，令牌立即失效。代理凭据写入 `/etc/yunling-agent/credentials.json`，文件权限 `0600`。

- [x] **Step 4: 实现 5 秒心跳与 15 秒离线判定**

代理每 5 秒发送单调递增序号的心跳。中央服务以收到时间为准，连续 15 秒没有心跳时把服务器标记为离线，并发布 `server.offline` 调度事件。

- [x] **Step 5: 运行代理与服务器测试**

Run: `go test ./internal/server ./internal/agentprotocol ./internal/agent -v`  
Expected: PASS。

- [x] **Step 6: 提交代理基础能力**

```bash
git add internal/agent internal/agentprotocol internal/server cmd/agent api/openapi.yaml cmd/api/main.go
git commit -m "feat: 添加代理注册心跳和资源采集"
```

---

### Task 5: 实现服务器管理与运行总览控制台

**Files:**
- Create: `internal/server/query.go`
- Create: `internal/server/http_test.go`
- Create: `apps/web/src/features/dashboard/DashboardPage.tsx`
- Create: `apps/web/src/features/dashboard/DashboardPage.test.tsx`
- Create: `apps/web/src/features/servers/ServersPage.tsx`
- Create: `apps/web/src/features/servers/ServerDrawer.tsx`
- Create: `apps/web/src/api/client.ts`
- Create: `migrations/000003_server_management.up.sql`
- Create: `migrations/000003_server_management.down.sql`
- Modify: `api/openapi.yaml`
- Modify: `apps/web/src/app/App.tsx`

**Interfaces:**
- Consumes: `server.Registry` 和 PostgreSQL 服务器快照。
- Produces: `GET /api/dashboard` 返回 `onlineServers`、`totalServers`、`runningRuns`、`queuedRuns`、`todaySuccessRate`、`servers`、`recentEvents`。
- Produces: `GET /api/servers`、`PATCH /api/servers/{id}`，支持名称、标签、权重、停用和排空。

- [x] **Step 1: 写 Dashboard API 失败测试**

```go
func TestDashboardCountsQueuedRuns(t *testing.T) {
    h := newDashboardHandler(fakeDashboardQuery{queued: 12, running: 6})
    rec := performGET(t, h, "/api/dashboard")
    require.Equal(t, http.StatusOK, rec.Code)
    require.JSONEq(t, `{"onlineServers":3,"totalServers":4,"runningRuns":6,"queuedRuns":12,"todaySuccessRate":98.4,"servers":[],"recentEvents":[]}`, rec.Body.String())
}
```

- [x] **Step 2: 写控制台排队规则展示失败测试**

```tsx
it('把排队作为控制台运行状态而非独立页面', async () => {
  render(<DashboardPage />)
  expect(await screen.findByText('排队任务')).toBeVisible()
  expect(screen.getByText('队列自动调度已开启')).toBeVisible()
  expect(screen.queryByRole('navigation', { name: '排队队列' })).not.toBeInTheDocument()
})
```

- [x] **Step 3: 实现 Dashboard 查询和中文页面**

页面严格沿用已确认的深绿色控制台结构：左侧导航、四项指标、服务器负载、实时任务、脚本同步摘要和最近动态。实时任务的“调度信息”列显示“空闲内存最高”“已缓存脚本版本”或“暂无节点满足 4 核 / 8 GB；资源空闲后自动运行”。

- [x] **Step 4: 实现服务器列表、标签、停用和排空**

排空只阻止新任务分配，不终止当前任务；停用会断开代理会话并阻止新连接，恢复启用后代理可重新连接。

- [x] **Step 5: 运行 API、组件与可访问性测试**

Run: `go test ./internal/server -v`  
Expected: PASS。

Run: `npm --workspace apps/web test -- --run src/features/dashboard src/features/servers`  
Expected: PASS。

- [x] **Step 6: 提交运行控制台**

```bash
git add internal/server apps/web/src/features/dashboard apps/web/src/features/servers apps/web/src/api api/openapi.yaml apps/web/src/app/App.tsx
git commit -m "feat: 添加服务器管理和运行总览"
```

---

### Task 6: 实现不可变脚本版本与对象存储

**Files:**
- Create: `internal/artifact/store.go`
- Create: `internal/artifact/minio.go`
- Create: `internal/script/service.go`
- Create: `internal/script/service_test.go`
- Create: `internal/script/http.go`
- Create: `apps/web/src/features/scripts/ScriptsPage.tsx`
- Create: `apps/web/src/features/scripts/ScriptEditorPage.tsx`
- Create: `apps/web/src/features/scripts/ScriptEditorPage.test.tsx`
- Modify: `api/openapi.yaml`

**Interfaces:**
- Produces: `artifact.Store.Put(ctx, key string, body io.Reader, size int64, sha256 string) error`。
- Produces: `artifact.Store.Open(ctx, key string) (io.ReadCloser, error)`。
- Produces: `script.Service.Publish(ctx, PublishInput) (script.Version, error)`。
- Produces: `POST /api/scripts`、`POST /api/scripts/import`、`PUT /api/scripts/{id}/draft`、`POST /api/scripts/{id}/publish`、`GET /api/scripts/{id}/versions`、`POST /api/scripts/{id}/rollback`。

- [x] **Step 1: 写不可变版本与内容去重失败测试**

```go
func TestPublishCreatesImmutableVersion(t *testing.T) {
    svc := newScriptService()
    v1, err := svc.Publish(ctx, script.PublishInput{ScriptID: id, Content: []byte("echo 1"), Runtime: "bash", AuthorID: userID})
    require.NoError(t, err)
    v2, err := svc.Publish(ctx, script.PublishInput{ScriptID: id, Content: []byte("echo 2"), Runtime: "bash", AuthorID: userID})
    require.NoError(t, err)
    require.Equal(t, 1, v1.Number)
    require.Equal(t, 2, v2.Number)
    require.NotEqual(t, v1.SHA256, v2.SHA256)
    require.Equal(t, []byte("echo 1"), readArtifact(t, v1.ArtifactKey))
}
```

- [x] **Step 2: 运行测试并确认失败**

Run: `go test ./internal/script ./internal/artifact -v`  
Expected: FAIL，缺少 Store 与 Publish。

- [x] **Step 3: 实现 SHA-256、对象键和事务发布**

对象键固定为 `scripts/{scriptID}/{sha256}.tar.gz`。先上传并校验对象，再在同一数据库事务内创建版本与发布事件；数据库失败时保留内容寻址对象，由每日无引用对象清理任务回收。

- [x] **Step 4: 实现创建、文件导入、编辑、发布、版本比较和回滚页面**

文件导入只接受配置允许的文本脚本扩展名和大小上限，导入内容先成为草稿。回滚不修改旧版本，而是将历史内容重新发布为新版本。发布表单必须填写中文发布说明，并选择全部兼容服务器、服务器组、标签集合或按需分发。

- [x] **Step 5: 运行脚本服务和 Web 测试**

Run: `go test ./internal/script ./internal/artifact -v`  
Expected: PASS。

Run: `npm --workspace apps/web test -- --run src/features/scripts`  
Expected: PASS。

- [x] **Step 6: 提交脚本版本能力**

```bash
git add internal/artifact internal/script apps/web/src/features/scripts api/openapi.yaml
git commit -m "feat: 添加脚本版本发布和回滚"
```

---

### Task 7: 实现代理脚本同步、原子切换与漂移检测

**Files:**
- Create: `internal/agentprotocol/sync.go`
- Create: `internal/script/sync_service.go`
- Create: `internal/script/sync_service_test.go`
- Create: `internal/executor/cache.go`
- Create: `internal/executor/cache_test.go`
- Create: `internal/executor/discovery.go`
- Create: `internal/executor/discovery_test.go`
- Create: `internal/executor/drift.go`
- Create: `apps/web/src/features/scripts/SyncStatusPanel.tsx`

**Interfaces:**
- Produces: `agentprotocol.SyncCommand{ScriptID, VersionID, ArtifactURL, SHA256}`。
- Produces: `agentprotocol.SyncResult{ScriptID, VersionID, State, ErrorCode}`。
- Produces: `executor.Cache.Ensure(ctx, command) (absolutePath string, error)`。
- Produces: `executor.Discovery.List(ctx, allowedRoots []string) ([]executor.DiscoveredScript, error)`，结果只包含允许目录内的普通文件。
- Produces: 同步状态 `pending`、`downloading`、`ready`、`failed`、`drifted`。

- [x] **Step 1: 写校验失败不替换旧版本的测试**

```go
func TestEnsureKeepsCurrentVersionWhenChecksumFails(t *testing.T) {
    cache := newTestCache(t, downloaderReturning([]byte("corrupted")))
    oldPath := cache.seedCurrent("script-1", "v1", []byte("echo old"))
    _, err := cache.Ensure(ctx, syncCommand("script-1", "v2", sha256Of("expected")))
    require.ErrorIs(t, err, executor.ErrChecksumMismatch)
    require.Equal(t, []byte("echo old"), mustRead(t, oldPath))
}
```

```go
func TestDiscoveryRejectsSymlinkOutsideAllowedRoot(t *testing.T) {
    root := t.TempDir()
    outside := writeTempScript(t, "echo outside")
    require.NoError(t, os.Symlink(outside, filepath.Join(root, "escape.sh")))
    found, err := executor.NewDiscovery().List(ctx, []string{root})
    require.NoError(t, err)
    require.Empty(t, found)
}
```

- [x] **Step 2: 运行测试并确认失败**

Run: `go test ./internal/executor ./internal/script -run 'TestEnsure|TestSync' -v`  
Expected: FAIL，缺少 Cache 与同步服务。

- [x] **Step 3: 实现临时下载、校验和原子切换**

代理写入 `<cache>/.staging/{versionID}`，校验 SHA-256 后以原子重命名切换 `<cache>/scripts/{scriptID}/{versionID}`，最后更新只包含版本号和校验值的 `manifest.json`。失败时删除 staging 内容，当前版本保持不变。

- [x] **Step 4: 实现允许目录扫描与首次导入**

代理配置显式列出 `allowed_script_roots`。Discovery 使用解析后的绝对路径检查每个候选文件仍位于允许根目录，拒绝符号链接越界、设备文件和超过上限的文件。控制台可查看发现结果并选择导入，导入内容进入中央草稿，发布后才成为纳管版本。

- [x] **Step 5: 实现漂移扫描和服务器隔离**

代理每 60 秒校验当前清单。发现漂移后发送 `SyncResult{State:"drifted"}`；中央服务禁止该服务器接收对应脚本的新任务，自动重新同步，成功后解除该脚本级隔离。

- [x] **Step 6: 运行同步、目录边界和漂移测试**

Run: `go test ./internal/executor ./internal/script -v`  
Expected: PASS。

- [x] **Step 7: 提交脚本同步能力**

```bash
git add internal/agentprotocol internal/script internal/executor apps/web/src/features/scripts/SyncStatusPanel.tsx
git commit -m "feat: 添加脚本同步和漂移检测"
```

---

### Task 8: 实现任务定义、Cron 计划与确定版本解析

**Files:**
- Create: `internal/task/service.go`
- Create: `internal/task/service_test.go`
- Create: `internal/task/schedule.go`
- Create: `internal/task/schedule_test.go`
- Create: `internal/task/http.go`
- Create: `apps/web/src/features/tasks/TasksPage.tsx`
- Create: `apps/web/src/features/tasks/TaskEditorPage.tsx`
- Create: `apps/web/src/features/tasks/TaskEditorPage.test.tsx`
- Modify: `api/openapi.yaml`

**Interfaces:**
- Produces: `task.Definition{ScriptID, VersionPolicy, Parameters, SecretRefs, Priority, RequiredLabels, Resources, MaxConcurrency, Timeout, MaxWait, RetryPolicy, Idempotent, Enabled}`。
- Produces: `task.Service.Trigger(ctx, definitionID, Trigger) (task.Run, error)`，返回的 Run 必须包含确定的 `ScriptVersionID`。
- Produces: `task.ScheduleDue(ctx, now time.Time) ([]task.Run, error)`。
- Produces: 任务 CRUD、启停、手动执行和 Cron 校验接口。

- [ ] **Step 1: 写“最新版本在创建运行时锁定”的失败测试**

```go
func TestTriggerResolvesLatestVersionOnce(t *testing.T) {
    scripts := fakeVersions{latest: versionID("v12")}
    svc := task.NewService(repo, scripts, clock)
    run, err := svc.Trigger(ctx, definitionID, task.TriggerManual)
    require.NoError(t, err)
    require.Equal(t, versionID("v12"), run.ScriptVersionID)
    scripts.latest = versionID("v13")
    require.Equal(t, versionID("v12"), repo.mustGet(run.ID).ScriptVersionID)
}
```

- [ ] **Step 2: 写 Cron 去重失败测试**

```go
func TestScheduleDueDoesNotDuplicateSameFireTime(t *testing.T) {
    scheduler := newScheduleService("0 2 * * *")
    first, err := scheduler.ScheduleDue(ctx, mustTime("2026-08-28T02:00:00+08:00"))
    require.NoError(t, err)
    second, err := scheduler.ScheduleDue(ctx, mustTime("2026-08-28T02:00:10+08:00"))
    require.NoError(t, err)
    require.Len(t, first, 1)
    require.Empty(t, second)
}
```

- [ ] **Step 3: 实现任务服务与时区明确的 Cron**

所有 Cron 规则保存 IANA 时区，默认 `Asia/Shanghai`。创建执行实例时解析版本、复制资源与重试配置，写入 `queued` 事件并发布 `run.queued` 调度事件。

- [ ] **Step 4: 实现全中文任务编辑器**

表单包含脚本版本策略、参数、服务器标签、CPU、内存、磁盘、优先级、最大并发、超时、最大等待、幂等声明和重试次数。停用时提供单独复选项“同时取消当前排队任务”，默认不勾选。

- [ ] **Step 5: 运行任务与 Cron 测试**

Run: `go test ./internal/task -v`  
Expected: PASS。

Run: `npm --workspace apps/web test -- --run src/features/tasks`  
Expected: PASS。

- [ ] **Step 6: 提交任务定义能力**

```bash
git add internal/task apps/web/src/features/tasks api/openapi.yaml
git commit -m "feat: 添加任务定义和定时计划"
```

---

### Task 9: 实现等待队列、候选筛选、评分和资源租约

**Files:**
- Create: `internal/scheduler/types.go`
- Create: `internal/scheduler/filter.go`
- Create: `internal/scheduler/filter_test.go`
- Create: `internal/scheduler/score.go`
- Create: `internal/scheduler/score_test.go`
- Create: `internal/scheduler/queue.go`
- Create: `internal/scheduler/queue_test.go`
- Create: `internal/scheduler/service.go`
- Create: `internal/scheduler/service_test.go`
- Create: `internal/store/redis/lease.go`
- Create: `internal/store/redis/lease_test.go`
- Create: `cmd/scheduler/main.go`

**Interfaces:**
- Produces: `scheduler.Filter(run task.Run, servers []server.Snapshot) []scheduler.Candidate`。
- Produces: `scheduler.Score(run task.Run, candidate scheduler.Candidate) int64`。
- Produces: `scheduler.LeaseStore.TryReserve(ctx, LeaseRequest) (Lease, bool, error)`。
- Produces: `scheduler.Service.ScheduleOne(ctx, runID) (scheduler.Outcome, error)`，Outcome 为 `assigned` 或 `queued`。

- [ ] **Step 1: 写资源不足保持排队的失败测试**

```go
func TestScheduleOneKeepsRunQueuedWhenNoServerFits(t *testing.T) {
    svc := newScheduler(serverWithFreeResources(2, 4<<30))
    out, err := svc.ScheduleOne(ctx, runRequiring(4, 8<<30))
    require.NoError(t, err)
    require.Equal(t, scheduler.OutcomeQueued, out)
    require.Equal(t, task.Queued, svc.runs.state)
    require.Empty(t, svc.assignments.items)
}
```

- [ ] **Step 2: 写资源释放唤醒队列的失败测试**

```go
func TestResourceReleasedWakesHighestPriorityWaitingRun(t *testing.T) {
    svc := newScheduler(busyServer())
    low := svc.queue.push(runWithPriority(10, timeAt(1)))
    high := svc.queue.push(runWithPriority(50, timeAt(2)))
    svc.HandleEvent(ctx, scheduler.Event{Type: scheduler.ResourceReleased})
    require.Equal(t, high.ID, svc.assignments.items[0].RunID)
    require.Equal(t, task.Queued, svc.runs.get(low.ID).State)
}
```

- [ ] **Step 3: 写原子租约防超卖测试**

```go
func TestTryReserveAllowsOnlyOneConcurrentWinner(t *testing.T) {
    store := newRedisLeaseStore(t, resources(4, 8<<30))
    results := runConcurrently(2, func() bool {
        _, ok, err := store.TryReserve(ctx, request(4, 8<<30))
        require.NoError(t, err)
        return ok
    })
    require.Equal(t, 1, countTrue(results))
}
```

- [ ] **Step 4: 实现筛选、确定性评分和 Redis Lua 租约**

筛选顺序固定为：在线与未排空、标签、运行环境、并发、脚本隔离、CPU、内存、磁盘。评分使用整数权重：剩余内存 35%、剩余 CPU 25%、低运行数 20%、脚本已缓存 15%、近期公平性 5%；相同分数按服务器 ID 排序，保证测试可重复。

租约 Lua 脚本在一个 Redis 操作中检查可用资源、扣减 CPU/内存/磁盘并写入带过期时间的租约键；失败不产生部分扣减。

- [ ] **Step 5: 实现事件唤醒与 15 秒兜底扫描**

监听 `run.queued`、`resource.released`、`server.online`、`server.changed` 和 `script.ready`。每 15 秒按优先级降序、入队时间升序扫描排队任务；超过 `MaxWait` 的任务迁移到 `expired`。

- [ ] **Step 6: 运行调度器与 Redis 集成测试**

Run: `go test ./internal/scheduler ./internal/store/redis -v`  
Expected: PASS，包含并发租约测试。

- [ ] **Step 7: 提交调度核心**

```bash
git add internal/scheduler internal/store/redis cmd/scheduler
git commit -m "feat: 添加资源感知调度和等待队列"
```

---

### Task 10: 实现代理隔离执行、取消与超时

**Files:**
- Create: `internal/agentprotocol/assignment.go`
- Create: `internal/executor/command.go`
- Create: `internal/executor/command_test.go`
- Create: `internal/executor/runner.go`
- Create: `internal/executor/runner_test.go`
- Create: `internal/executor/systemd.go`
- Create: `internal/executor/process.go`
- Modify: `internal/agent/client.go`
- Create: `deploy/agent/yunling-agent.service`
- Create: `deploy/agent/install.sh`

**Interfaces:**
- Produces: `agentprotocol.Assignment{RunID, ExecutionToken, ScriptVersionID, Runtime, ScriptPath, Arguments, Environment, Resources, Timeout}`。
- Produces: `executor.Runner.Start(ctx, assignment) (<-chan executor.Event, error)`。
- Produces: `executor.Runner.Cancel(ctx, runID, executionToken) error`。
- Produces: `executor.Event{Sequence, Type, OccurredAt, ExitCode, Message}`。

- [ ] **Step 1: 写参数不经过 shell 拼接的失败测试**

```go
func TestBuildCommandKeepsArgumentBoundaries(t *testing.T) {
    cmd, err := executor.BuildCommand("python3", "/cache/script.py", []string{"--name", "a; rm -rf /"})
    require.NoError(t, err)
    require.Equal(t, "python3", cmd.Path)
    require.Equal(t, []string{"python3", "/cache/script.py", "--name", "a; rm -rf /"}, cmd.Args)
}
```

- [ ] **Step 2: 写超时终止整个进程组的失败测试**

```go
func TestRunnerKillsProcessGroupOnTimeout(t *testing.T) {
    launcher := newFakeLauncher(blockUntilKilled())
    runner := executor.NewRunner(launcher, time.Millisecond)
    events, err := runner.Start(ctx, assignmentWithTimeout(time.Millisecond))
    require.NoError(t, err)
    require.Contains(t, collectTypes(events), executor.EventTimedOut)
    require.True(t, launcher.processGroupKilled)
}
```

- [ ] **Step 3: 实现运行环境白名单和独立工作目录**

运行环境只接受代理注册时上报并由管理员允许的可执行文件。每次执行目录为 `/var/lib/yunling-agent/runs/{runID}`，脚本缓存以只读方式引用，临时输出只写入执行目录。

- [ ] **Step 4: 实现 systemd 临时单元资源限制**

Linux 使用 `systemd-run --unit=yunling-run-{runID} --uid=yunling-runner --wait --collect` 创建临时服务单元，并通过 `--property` 设置 `CPUQuota`、`MemoryMax`、`TasksMax` 和 `RuntimeMaxSec`。测试使用 `Launcher` 接口替代真实 systemd，集成环境再验证真实限制。

- [ ] **Step 5: 实现取消与超时**

取消先发送 SIGTERM，等待 10 秒后对整个进程组发送 SIGKILL。只有 RunID 与 ExecutionToken 同时匹配当前执行，代理才接受取消请求。

- [ ] **Step 6: 运行执行器测试**

Run: `go test ./internal/executor ./internal/agent -v`  
Expected: PASS。

- [ ] **Step 7: 提交代理执行能力**

```bash
git add internal/agent internal/agentprotocol internal/executor cmd/agent deploy/agent
git commit -m "feat: 添加代理隔离执行和任务终止"
```

---

### Task 11: 实现状态对账、实时日志与执行记录

**Files:**
- Create: `internal/logstream/service.go`
- Create: `internal/logstream/service_test.go`
- Create: `internal/logstream/spool.go`
- Create: `internal/logstream/spool_test.go`
- Create: `internal/logstream/archive.go`
- Create: `internal/task/event_service.go`
- Create: `internal/task/event_service_test.go`
- Create: `internal/task/reconcile.go`
- Create: `internal/task/reconcile_test.go`
- Create: `apps/web/src/features/runs/RunsPage.tsx`
- Create: `apps/web/src/features/runs/RunDetailPage.tsx`
- Create: `apps/web/src/features/runs/RunDetailPage.test.tsx`
- Create: `apps/web/src/api/events.ts`
- Modify: `api/openapi.yaml`

**Interfaces:**
- Produces: `logstream.Service.Accept(ctx, chunk LogChunk) (nextSequence uint64, error)`。
- Produces: `task.EventService.Apply(ctx, event agentprotocol.RunEvent) error`，按执行令牌和序号去重。
- Produces: `task.Reconciler.Reconcile(ctx, report agentprotocol.RunningReport) error`。
- Produces: `logstream.Archiver.Archive(ctx, runID task.RunID) (artifactKey string, error)`。
- Produces: `GET /api/runs`、`GET /api/runs/{id}`、`GET /api/runs/{id}/events` SSE、`POST /api/runs/{id}/cancel`、`POST /api/runs/{id}/retry`。

- [ ] **Step 1: 写日志续传和事件去重失败测试**

```go
func TestAcceptAcknowledgesNextMissingSequence(t *testing.T) {
    svc := newLogService()
    next, err := svc.Accept(ctx, chunk(1, "第一行\n"))
    require.NoError(t, err)
    require.Equal(t, uint64(2), next)
    next, err = svc.Accept(ctx, chunk(1, "第一行\n"))
    require.NoError(t, err)
    require.Equal(t, uint64(2), next)
    require.Equal(t, "第一行\n", svc.fullText())
}
```

- [ ] **Step 2: 写失联后状态待确认测试**

```go
func TestOfflineRunningServerMarksRunUnknown(t *testing.T) {
    reconciler := newReconciler(runInState(task.Running))
    require.NoError(t, reconciler.ServerOffline(ctx, serverID))
    require.Equal(t, task.Unknown, reconciler.run.State)
    require.Empty(t, reconciler.retryQueue)
}
```

- [ ] **Step 3: 实现本地日志分块与确认后清理**

代理日志块默认 64 KiB，包含 RunID、ExecutionToken、Sequence、Stream 和内容。服务端只接受下一个序号或已存在的重复序号；代理收到 `nextSequence` 后删除更小序号的本地缓冲。

- [ ] **Step 4: 实现状态对账和幂等重试限制**

代理重连后发送正在运行令牌列表。中央将匹配任务恢复为 `running`；代理确认已结束的任务补交终态；无法确认的任务保持 `unknown`。只有 `Idempotent=true` 且重试策略允许、并确认原进程不存在时，才创建新的重试执行实例。

- [ ] **Step 5: 实现日志归档和运行产物索引**

完成任务的日志超过数据库保留阈值后压缩写入 `runs/{runID}/logs.ndjson.gz`，数据库保留对象键、大小、校验值和时间范围。代理上报的运行产物必须匹配任务定义允许的 glob，逐个校验大小并写入 `runs/{runID}/artifacts/{name}`。

- [ ] **Step 6: 实现执行记录和实时日志页面**

详情页展示中文状态时间线、服务器、版本、资源、参数摘要、退出码和流式日志。日志支持暂停自动滚动、关键词过滤、下载和清屏显示；清屏只影响浏览器，不删除服务端日志。

- [ ] **Step 7: 运行日志、归档、对账和 Web 测试**

Run: `go test ./internal/logstream ./internal/task -v`  
Expected: PASS。

Run: `npm --workspace apps/web test -- --run src/features/runs`  
Expected: PASS。

- [ ] **Step 8: 提交状态与日志能力**

```bash
git add internal/logstream internal/task apps/web/src/features/runs apps/web/src/api/events.ts api/openapi.yaml
git commit -m "feat: 添加任务状态对账和实时日志"
```

---

### Task 12: 实现敏感参数、审计和安全边界

**Files:**
- Create: `internal/secret/service.go`
- Create: `internal/secret/service_test.go`
- Create: `internal/secret/redactor.go`
- Create: `internal/secret/redactor_test.go`
- Create: `internal/audit/service.go`
- Create: `internal/audit/service_test.go`
- Create: `internal/alert/service.go`
- Create: `internal/alert/service_test.go`
- Modify: `internal/server/registry.go`
- Create: `apps/web/src/features/settings/SecretsPage.tsx`
- Create: `apps/web/src/features/settings/MembersPage.tsx`
- Create: `apps/web/src/features/settings/AuditPage.tsx`
- Create: `apps/web/src/features/settings/AlertsPanel.tsx`
- Modify: `api/openapi.yaml`

**Interfaces:**
- Produces: `secret.Service.Create(ctx, name string, plaintext []byte) (secret.Metadata, error)`。
- Produces: `secret.Service.ResolveForRun(ctx, refs []secret.ID) (map[string]string, error)`，仅代理下发路径可调用。
- Produces: `secret.Redactor.Mask(text []byte, values [][]byte) []byte`。
- Produces: `audit.Service.Record(ctx, Event) error`。
- Produces: `alert.Service.Raise(ctx, alert.Event) error`，同一资源与错误代码在 5 分钟窗口内合并。
- Produces: `POST /api/servers/{id}/credentials/rotate` 和 `POST /api/servers/{id}/credentials/revoke`。

- [ ] **Step 1: 写密文与日志脱敏失败测试**

```go
func TestCreateNeverStoresPlaintext(t *testing.T) {
    repo := newMemorySecretRepo()
    svc := secret.NewService(repo, fixedKeyProvider())
    _, err := svc.Create(ctx, "数据库密码", []byte("very-secret"))
    require.NoError(t, err)
    require.NotContains(t, string(repo.ciphertext), "very-secret")
}

func TestRedactorMasksExactAndEncodedValues(t *testing.T) {
    got := secret.NewRedactor().Mask([]byte("pwd=very-secret encoded=dmVyeS1zZWNyZXQ="), [][]byte{[]byte("very-secret")})
    require.Equal(t, []byte("pwd=****** encoded=******"), got)
}
```

```go
func TestRaiseMergesDuplicateAlertWithinWindow(t *testing.T) {
    svc := alert.NewService(memoryAlertRepo(), fixedClock())
    event := alert.Event{ResourceID: "server-1", Code: "agent_offline", Message: "代理已离线"}
    require.NoError(t, svc.Raise(ctx, event))
    require.NoError(t, svc.Raise(ctx, event))
    require.Equal(t, 1, svc.repo.Count())
    require.Equal(t, 2, svc.repo.First().Occurrences)
}
```

- [ ] **Step 2: 运行测试并确认失败**

Run: `go test ./internal/secret ./internal/audit -v`  
Expected: FAIL，缺少加密、脱敏和审计实现。

- [ ] **Step 3: 实现信封加密和不可回显接口**

主密钥从 `YUNLING_MASTER_KEY_FILE` 指向的文件读取，不放入数据库和环境变量。每个敏感值使用随机数据密钥经 AES-256-GCM 加密，数据密钥再由主密钥加密。API 永远只返回 ID、名称、创建人和更新时间。

- [ ] **Step 4: 实现日志脱敏、关键操作审计和系统告警**

日志进入持久化前对明文、Base64 和 URL 编码形式执行脱敏。登录、脚本发布、回滚、同步、执行、终止、重试、密钥创建、成员与权限变更均写入只追加审计表。服务器离线、脚本连续同步失败、版本漂移、任务长时间排队和日志缓冲接近上限时生成中文告警，同一资源与错误代码在 5 分钟内合并计数。代理凭据轮换时先签发新凭据，代理确认新连接后吊销旧凭据；紧急吊销立即断开当前代理并把节点标记离线。

- [ ] **Step 5: 实现成员、密钥和审计页面**

只读成员不可看到创建密钥按钮；脚本开发者可引用密钥但不可读取值；运维人员可执行使用密钥的任务；管理员可创建、轮换和吊销密钥。

- [ ] **Step 6: 运行安全测试**

Run: `go test ./internal/secret ./internal/audit ./internal/alert ./internal/auth -v`  
Expected: PASS。

Run: `npm --workspace apps/web test -- --run src/features/settings`  
Expected: PASS。

- [ ] **Step 7: 提交安全与审计**

```bash
git add internal/secret internal/audit internal/alert internal/auth apps/web/src/features/settings api/openapi.yaml
git commit -m "feat: 添加敏感参数和审计日志"
```

---

### Task 13: 完成部署、恢复测试和端到端验收

**Files:**
- Create: `deploy/docker-compose.yml`
- Create: `deploy/.env.example`
- Create: `deploy/Caddyfile`
- Create: `deploy/README.md`
- Create: `tests/integration/scheduler_recovery_test.go`
- Create: `tests/integration/agent_reconnect_test.go`
- Create: `apps/web/e2e/script-run.spec.ts`
- Create: `apps/web/e2e/queue-wakeup.spec.ts`
- Create: `apps/web/e2e/fixtures.ts`
- Create: `apps/web/playwright.config.ts`
- Modify: `Makefile`
- Create: `README.md`

**Interfaces:**
- Consumes: Tasks 1–12 的 API、调度器、代理和 Web 控制台。
- Produces: `docker compose -f deploy/docker-compose.yml up -d` 可启动 Web、API、Scheduler、PostgreSQL、Redis、MinIO 和 Caddy。
- Produces: `make test`、`make test-integration`、`make test-e2e`、`make build`。

- [ ] **Step 1: 先写排队唤醒端到端失败测试**

```ts
test('资源不足时排队，服务器释放资源后自动运行', async ({ page }) => {
  await seedServer({ cpu: 2, memoryGiB: 4, running: 1 })
  await createTask({ name: '图片批量压缩', cpu: 4, memoryGiB: 8 })
  await page.goto('/tasks')
  await page.getByRole('button', { name: '立即执行' }).click()
  await expect(page.getByText('排队中')).toBeVisible()
  await releaseServerResources({ cpu: 8, memoryGiB: 16 })
  await expect(page.getByText('执行中')).toBeVisible()
})
```

- [ ] **Step 2: 写中央服务重启恢复失败测试**

```go
func TestSchedulerRebuildsQueueAndLeasesAfterRedisFlush(t *testing.T) {
    env := startIntegrationEnvironment(t)
    queued := env.CreateQueuedRun(resources(4, 8<<30))
    env.FlushRedis()
    env.RestartScheduler()
    require.Eventually(t, func() bool { return env.QueueContains(queued.ID) }, 5*time.Second, 100*time.Millisecond)
}
```

- [ ] **Step 3: 实现 Docker Compose 和 TLS 入口**

Compose 为每个服务设置健康检查、只读容器文件系统和命名卷。PostgreSQL、Redis、MinIO 不暴露公网端口；只有 Caddy 暴露 80/443。`.env.example` 只列变量名和生成方式，不包含任何真实 IP、账号、密码或密钥。

- [ ] **Step 4: 实现 Makefile 验证入口**

```make
test:
	go test ./...
	npm --workspace apps/web test -- --run

build:
	go build ./cmd/api ./cmd/scheduler ./cmd/agent
	npm --workspace apps/web run build
```

`test-integration` 启动临时 PostgreSQL、Redis 和 MinIO 后运行 `go test ./tests/integration -v`；`test-e2e` 启动完整 Compose 测试栈后运行 Playwright。

- [ ] **Step 5: 完成首次部署文档**

部署文档明确：腾讯云安全组只开放 80/443；代理服务器使用一次性注册令牌；正式接入创建 `yunling-agent` 与 `yunling-runner` 专用账号；用户此前提供的 root 密码不得写入任何配置文件，完成代理安装后必须轮换。

- [ ] **Step 6: 运行全量验证**

Run: `make test`  
Expected: 所有 Go 单元测试、React/Vitest 测试通过。

Run: `make test-integration`  
Expected: 调度、同步、断线、Redis 重建和代理重连测试通过。

Run: `make test-e2e`  
Expected: 登录、脚本发布、同步、手动执行、资源不足排队、资源释放自动运行、日志查看、取消和回滚流程通过。

Run: `make build`  
Expected: 生成三个 Go 可执行文件和 `apps/web/dist`，命令退出码为 0。

- [ ] **Step 7: 提交部署与验收**

```bash
git add deploy tests/integration apps/web/e2e apps/web/playwright.config.ts Makefile README.md
git commit -m "feat: 完成部署和端到端验收"
```

---

## 2. 实施检查点

- Task 1–3 完成后：可登录的全中文控制台骨架与稳定数据模型。
- Task 4–5 完成后：首台模拟代理可注册、上报资源并出现在运行总览。
- Task 6–7 完成后：脚本可发布、同步、校验、回滚和检测漂移。
- Task 8–9 完成后：任务可手动或定时入队，资源不足保持排队，资源变化自动分配。
- Task 10–11 完成后：代理可隔离执行脚本并实时回传状态与日志。
- Task 12–13 完成后：权限、密钥、审计、恢复、部署和端到端验收闭环。

每个检查点必须先通过该阶段全部测试，再进入下一阶段。首次连接真实京东云服务器安排在 Task 13 的测试环境验证通过之后，且只使用新建的专用代理账号和一次性注册令牌。
