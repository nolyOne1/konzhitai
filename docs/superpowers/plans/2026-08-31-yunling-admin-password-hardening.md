# 云令管理员改密与会话安全 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为已登录用户提供安全的自助改密能力，原子更新密码、撤销其他会话、记录审计，并在中文运维中心完成操作。

**Architecture:** 在 `internal/auth` 内新增独立的密码修改服务和 PostgreSQL 事务仓储；API 使用现有会话中间件保护新接口，并增加同源、内容类型和持久化限速检查。Web 先建立“运维中心”页面，在其中放置账号安全面板，供后续飞书和备份计划继续扩展。

**Tech Stack:** Go 1.27、pgx v5、PostgreSQL 18、Argon2id、React 19、TypeScript 7、Vitest、Playwright。

**Spec:** `docs/superpowers/specs/2026-08-31-yunling-operations-hardening-design.md`

## Global Constraints

- 新密码至少 12 位，且不能与当前密码相同。
- 当前密码必须重新验证；失败响应不区分当前密码错误和新密码策略失败。
- 密码更新、其他会话撤销和 `auth.password.change` 审计必须在同一个 PostgreSQL 事务中完成。
- 当前会话必须保留，其他未过期会话立即失效。
- 接口必须使用 SameSite 会话 Cookie、显式 Origin 校验、`application/json` 校验和账号/IP 双维度限速。
- 请求体、密码、会话令牌和哈希不得进入日志、响应或审计详情。
- 成功响应必须包含 `Cache-Control: no-store`。
- 先写失败测试，再写最小实现；每个任务独立提交。

---

### Task 1: 持久化改密限速与事务仓储

**Files:**
- Create: `migrations/000010_password_change_security.up.sql`
- Create: `migrations/000010_password_change_security.down.sql`
- Create: `internal/auth/password_change_postgres.go`
- Create: `internal/auth/password_change_postgres_test.go`
- Modify: `internal/store/postgres/migrations_test.go`

**Interfaces:**
- Produces: `type PasswordChangeStore interface { PasswordHash(context.Context, string) (string, error); RegisterAttempt(context.Context, string, string, time.Time) (bool, error); CommitPasswordChange(context.Context, PasswordChangeCommit) error }`
- Produces: `type PasswordChangeCommit struct { UserID string; ExpectedHash string; NewHash string; CurrentSessionHash []byte; IPAddress string; ChangedAt time.Time }`
- Produces: 限速规则为同一用户或同一 IP 在固定 15 分钟窗口最多 5 次请求；窗口到期后重置计数。

- [ ] **Step 1: 写迁移失败测试**

在 `internal/store/postgres/migrations_test.go` 新增：

```go
func TestPasswordChangeMigrationCreatesRateLimitTable(t *testing.T) {
	db := startPostgres(t)
	applyMigrations(t, db)
	if !tableExists(t, db, "auth_rate_limits") {
		t.Fatal("改密安全迁移后应存在 auth_rate_limits")
	}
}
```

- [ ] **Step 2: 运行迁移测试确认失败**

Run: `go test ./internal/store/postgres -run TestPasswordChangeMigrationCreatesRateLimitTable -count=1`

Expected: FAIL，提示 `auth_rate_limits` 不存在。

- [ ] **Step 3: 添加向上和向下迁移**

`000010_password_change_security.up.sql` 使用以下结构：

```sql
CREATE TABLE IF NOT EXISTS schema_migrations (
    version integer PRIMARY KEY CHECK (version > 0),
    applied_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO schema_migrations (version)
SELECT generate_series(1, 10)
ON CONFLICT (version) DO NOTHING;

CREATE TABLE auth_rate_limits (
    scope text NOT NULL CHECK (scope IN ('password_user', 'password_ip')),
    subject_hash bytea NOT NULL,
    window_started_at timestamptz NOT NULL,
    attempts integer NOT NULL CHECK (attempts > 0),
    PRIMARY KEY (scope, subject_hash)
);

CREATE INDEX auth_rate_limits_window_idx
    ON auth_rate_limits (window_started_at);
```

`000010_password_change_security.down.sql`：

```sql
DROP TABLE IF EXISTS auth_rate_limits;
DELETE FROM schema_migrations WHERE version = 10;
DROP TABLE IF EXISTS schema_migrations;
```

- [ ] **Step 4: 运行迁移测试确认通过**

Run: `go test ./internal/store/postgres -run TestPasswordChangeMigrationCreatesRateLimitTable -count=1`

Expected: PASS。

- [ ] **Step 5: 写 PostgreSQL 仓储失败测试**

覆盖三个行为：第 6 次同用户尝试被拒绝；改密事务只撤销其他会话；事务同时追加无敏感详情的审计记录。核心断言：

```go
allowed, err := store.RegisterAttempt(ctx, userID, "203.0.113.8", now)
if err != nil || allowed {
	t.Fatalf("第六次尝试必须被限速：allowed=%v err=%v", allowed, err)
}

err = store.CommitPasswordChange(ctx, auth.PasswordChangeCommit{
	UserID: userID, ExpectedHash: oldHash, NewHash: newHash,
	CurrentSessionHash: currentHash, IPAddress: "203.0.113.8", ChangedAt: now,
})
if err != nil { t.Fatal(err) }
```

测试随后查询：用户哈希等于 `newHash`；当前会话 `revoked_at IS NULL`；另一会话已撤销；最新审计动作为 `auth.password.change` 且 `details = '{}'::jsonb`。

- [ ] **Step 6: 运行仓储测试确认失败**

Run: `go test ./internal/auth -run 'TestPostgresPasswordChange' -count=1`

Expected: FAIL，编译器报告 `undefined: NewPostgresPasswordChangeStore`。

- [ ] **Step 7: 实现事务仓储**

在 `password_change_postgres.go` 定义：

```go
type PasswordChangeCommit struct {
	UserID            string
	ExpectedHash       string
	NewHash            string
	CurrentSessionHash []byte
	IPAddress          string
	ChangedAt          time.Time
}

type PasswordChangeStore interface {
	PasswordHash(context.Context, string) (string, error)
	RegisterAttempt(context.Context, string, string, time.Time) (bool, error)
	CommitPasswordChange(context.Context, PasswordChangeCommit) error
}
```

`RegisterAttempt` 对用户 ID 和 IP 分别做 SHA-256，使用单个事务并以 PostgreSQL `ON CONFLICT DO UPDATE` upsert 两条限速记录，更新固定 15 分钟窗口；任一维度计数超过 5 即返回 `false`。`CommitPasswordChange` 必须：

1. `SELECT password_hash FROM users WHERE id=$1 FOR UPDATE`。
2. 比较数据库值与 `ExpectedHash`，不一致返回 `ErrPasswordChanged`。
3. 更新 `users.password_hash`。
4. 撤销 `user_id=$1 AND token_hash<>$2 AND revoked_at IS NULL` 的会话。
5. 删除该用户的 `password_user` 限速记录。
6. 向 `audit_logs` 追加 `auth.password.change`，`target_type='user'`，`target_id=userID`，空详情和请求 IP。
7. 提交事务。

- [ ] **Step 8: 运行仓储与迁移测试**

Run: `go test ./internal/auth ./internal/store/postgres -run 'PasswordChange|RateLimit' -count=1`

Expected: PASS。

- [ ] **Step 9: 提交数据库与仓储**

```bash
git add migrations/000010_password_change_security.* internal/auth/password_change_postgres.go internal/auth/password_change_postgres_test.go internal/store/postgres/migrations_test.go
git commit -m "feat: 建立安全改密事务仓储"
```

### Task 2: 改密服务、同源校验与 HTTP 接口

**Files:**
- Create: `internal/auth/password_change.go`
- Create: `internal/auth/password_change_test.go`
- Create: `internal/auth/password_http.go`
- Create: `internal/auth/password_http_test.go`
- Modify: `internal/auth/session.go`
- Modify: `cmd/api/main.go`
- Modify: `deploy/Caddyfile`
- Modify: `tests/integration/deployment_security_test.go`

**Interfaces:**
- Consumes: `PasswordChangeStore` 和 `PasswordChangeCommit`（Task 1）。
- Produces: `NewPasswordChangeService(store PasswordChangeStore, now func() time.Time) *PasswordChangeService`。
- Produces: `Change(ctx context.Context, principal Principal, sessionToken, currentPassword, newPassword, ip string) error`。
- Produces: `PasswordHandler(service *PasswordChangeService, allowedOrigin string) http.Handler`，处理 `POST /api/auth/password`。

- [ ] **Step 1: 写服务失败测试**

在 `password_change_test.go` 使用内存仓储覆盖：错误当前密码、少于 12 位、与当前密码相同、被限速、成功提交。成功断言：

```go
err := service.Change(ctx, auth.Principal{UserID: "user-1"}, "current-token",
	"correct-current-password", "new-password-2026", "203.0.113.8")
if err != nil { t.Fatal(err) }
if store.commit.UserID != "user-1" || len(store.commit.CurrentSessionHash) != sha256.Size {
	t.Fatalf("提交内容错误：%+v", store.commit)
}
```

- [ ] **Step 2: 运行服务测试确认失败**

Run: `go test ./internal/auth -run TestPasswordChangeService -count=1`

Expected: FAIL，提示 `NewPasswordChangeService` 不存在。

- [ ] **Step 3: 实现最小服务**

定义统一公开错误：

```go
var (
	ErrPasswordRejected = errors.New("当前密码或新密码不符合要求")
	ErrPasswordRateLimited = errors.New("操作过于频繁，请稍后再试")
	ErrPasswordChanged = errors.New("密码已被其他请求修改")
)
```

`Change` 的固定顺序为：注册限速尝试、读取哈希、验证当前密码、验证新密码长度、拒绝相同密码、生成 Argon2id 哈希、构造 `PasswordChangeCommit` 并提交。所有验证失败统一返回 `ErrPasswordRejected`；底层错误使用中文上下文包装，但不得包含输入值。

- [ ] **Step 4: 运行服务测试确认通过**

Run: `go test ./internal/auth -run TestPasswordChangeService -count=1`

Expected: PASS。

- [ ] **Step 5: 写 HTTP 失败测试**

覆盖：未认证 401、跨域 Origin 403、非 JSON 415、未知字段 400、错误密码 400、限速 429、成功 204 且 `Cache-Control: no-store`。成功请求：

```go
request := httptest.NewRequest(http.MethodPost, "https://aiwise.top/api/auth/password",
	strings.NewReader(`{"currentPassword":"correct-current-password","newPassword":"new-password-2026"}`))
request.Header.Set("Origin", "https://aiwise.top")
request.Header.Set("Content-Type", "application/json")
request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "current-token"})
request = request.WithContext(auth.WithPrincipal(request.Context(), auth.Principal{UserID: "user-1"}))
```

- [ ] **Step 6: 运行 HTTP 测试确认失败**

Run: `go test ./internal/auth -run TestPasswordHandler -count=1`

Expected: FAIL，提示 `PasswordHandler` 不存在。

- [ ] **Step 7: 实现 HTTP 处理器并接入 API**

`PasswordHandler` 使用 `http.MaxBytesReader(w, r.Body, 4096)`、`DisallowUnknownFields`，要求 `Content-Type` 的媒体类型等于 `application/json`，并要求 `Origin` 精确等于 `YUNLING_PUBLIC_URL` 的 scheme+host。Caddy 必须删除客户端自带的转发头并设置 `X-Forwarded-For: {remote_host}`；API 仅在 `YUNLING_TRUST_PROXY=true` 且自身未向宿主机发布端口时读取该单值头，否则只使用 `RemoteAddr`。部署安全测试同时断言 API 只位于受控 Docker 网络且 Caddy 覆盖转发头，避免伪造来源 IP。

在 `cmd/api/main.go` 新增独立 `passwordHandler`，数据库可用时通过现有 `protect` 闭包包装：

```go
passwordStore := auth.NewPostgresPasswordChangeStore(pool)
passwordService := auth.NewPasswordChangeService(passwordStore, time.Now)
passwordHandler = protect(auth.PasswordHandler(passwordService, os.Getenv("YUNLING_PUBLIC_URL")))
router.Handle("POST /api/auth/password", passwordHandler)
```

使用 Go ServeMux 的精确 method+path 路由；测试同时访问 `/api/auth/password/extra`，确认它不会被改密处理器接管。

- [ ] **Step 8: 运行认证包与 API 构建**

Run: `go test ./internal/auth ./cmd/api -count=1`

Expected: PASS。

- [ ] **Step 9: 提交服务与接口**

```bash
git add internal/auth/password_change.go internal/auth/password_change_test.go internal/auth/password_http.go internal/auth/password_http_test.go internal/auth/session.go cmd/api/main.go deploy/Caddyfile tests/integration/deployment_security_test.go
git commit -m "feat: 提供管理员安全改密接口"
```

### Task 3: 运维中心账号安全面板

**Files:**
- Create: `apps/web/src/features/operations/OperationsPage.tsx`
- Create: `apps/web/src/features/operations/OperationsPage.test.tsx`
- Create: `apps/web/src/features/operations/AccountSecurityPanel.tsx`
- Create: `apps/web/src/features/operations/AccountSecurityPanel.test.tsx`
- Modify: `apps/web/src/api/client.ts`
- Modify: `apps/web/src/app/App.tsx`
- Modify: `apps/web/src/app/App.test.tsx`
- Modify: `apps/web/src/app/styles.css`

**Interfaces:**
- Consumes: `POST /api/auth/password`（Task 2）。
- Produces: `changePassword(currentPassword: string, newPassword: string): Promise<void>`。
- Produces: `/operations` 路由和“运维中心”导航入口。
- Produces: `OperationsPage` 容器，后续计划在其中增加飞书和备份面板。

- [ ] **Step 1: 写 API 客户端失败测试**

在 `apps/web/src/api/client.test.ts` 断言：

```ts
await changePassword('current-password', 'new-password-2026')
expect(fetchMock).toHaveBeenCalledWith('/api/auth/password', expect.objectContaining({
  method: 'POST',
  credentials: 'same-origin',
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({ currentPassword: 'current-password', newPassword: 'new-password-2026' }),
}))
```

- [ ] **Step 2: 运行客户端测试确认失败**

Run: `npm --workspace apps/web test -- --run src/api/client.test.ts`

Expected: FAIL，提示 `changePassword` 未导出。

- [ ] **Step 3: 实现客户端方法**

在 `client.ts` 增加：

```ts
export async function changePassword(currentPassword: string, newPassword: string): Promise<void> {
  await request<void>('/api/auth/password', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ currentPassword, newPassword }),
  })
}
```

- [ ] **Step 4: 写账号安全面板失败测试**

覆盖字段可访问名称、少于 12 位前端拒绝、两次新密码不一致、成功后清空三个字段并显示“密码已更新，其他设备已退出”。禁止在 DOM 中回显输入值。

```tsx
await user.type(screen.getByLabelText('当前密码'), 'current-password')
await user.type(screen.getByLabelText('新密码'), 'new-password-2026')
await user.type(screen.getByLabelText('确认新密码'), 'new-password-2026')
await user.click(screen.getByRole('button', { name: '更新密码' }))
expect(await screen.findByRole('status')).toHaveTextContent('密码已更新，其他设备已退出')
```

- [ ] **Step 5: 运行面板测试确认失败**

Run: `npm --workspace apps/web test -- --run src/features/operations/AccountSecurityPanel.test.tsx`

Expected: FAIL，提示组件不存在。

- [ ] **Step 6: 实现运维中心与安全面板**

`OperationsPage` 先渲染页面标题和 `AccountSecurityPanel`。面板使用 `autocomplete="current-password"` 与 `autocomplete="new-password"`，错误区域获得焦点，提交期间禁用按钮，成功后用空字符串覆盖三个 state。不要把密码放进 URL、React Query 缓存或持久化存储。

在 `App.tsx` 增加：

```tsx
{ label: '运维中心', href: '/operations' }
<Route path="/operations" element={<OperationsPage />} />
```

样式沿用 `.panel`、`.form-grid`、`.primary-action`，只为安全面板增加局部类，保持移动端单列和可见焦点。

- [ ] **Step 7: 运行 Web 定向测试**

Run: `npm --workspace apps/web test -- --run src/api/client.test.ts src/features/operations/AccountSecurityPanel.test.tsx src/features/operations/OperationsPage.test.tsx src/app/App.test.tsx`

Expected: PASS。

- [ ] **Step 8: 提交中文界面**

```bash
git add apps/web/src/api/client.ts apps/web/src/api/client.test.ts apps/web/src/features/operations apps/web/src/app/App.tsx apps/web/src/app/App.test.tsx apps/web/src/app/styles.css
git commit -m "feat: 增加运维中心账号安全面板"
```

### Task 4: 端到端验证与改密运维文档

**Files:**
- Create: `apps/web/e2e/operations-security.spec.ts`
- Modify: `apps/web/e2e/fixtures.ts`
- Modify: `deploy/README.md`
- Modify: `deploy/PRODUCTION.md`

**Interfaces:**
- Consumes: `/operations` 页面和 `POST /api/auth/password`。
- Produces: 可重复执行的改密验收流程；生产文档只记录状态，不记录密码。

- [ ] **Step 1: 写端到端失败测试**

`operations-security.spec.ts` 使用 fixture 模拟会话和改密接口：

```ts
test('管理员改密后看到其他设备已退出提示', async ({ page }) => {
  await page.goto('/operations')
  await page.getByLabel('当前密码').fill('current-password')
  await page.getByLabel('新密码').fill('new-password-2026')
  await page.getByLabel('确认新密码').fill('new-password-2026')
  await page.getByRole('button', { name: '更新密码' }).click()
  await expect(page.getByRole('status')).toContainText('其他设备已退出')
})
```

- [ ] **Step 2: 运行 E2E 确认失败**

Run: `npm --workspace apps/web run test:e2e -- operations-security.spec.ts`

Expected: FAIL，直到 fixture 和页面流程完整。

- [ ] **Step 3: 完成 fixture 与部署文档**

fixture 对 `POST /api/auth/password` 返回 204，且断言请求体字段精确。`deploy/README.md` 增加生产步骤：备份数据库、部署加法迁移、通过控制台改密、使用新密码重新登录、确认旧密码失效、最后执行：

```bash
sudo test -f /root/yunling-initial-admin.txt
sudo rm -f /root/yunling-initial-admin.txt
sudo test ! -e /root/yunling-initial-admin.txt
```

`deploy/PRODUCTION.md` 新增明确的阶段记录：本地实现阶段写“改密接口尚未部署，初始凭据文件继续保留”；只有真正生产验收后才改成实际部署提交和文件删除结果。

- [ ] **Step 4: 运行完整本地验证**

Run: `go test ./... -count=1`

Expected: PASS。

Run: `npm --workspace apps/web test -- --run`

Expected: PASS。

Run: `npm --workspace apps/web run build`

Expected: PASS。

Run: `npm --workspace apps/web run test:e2e -- operations-security.spec.ts`

Expected: PASS。

Run: `git diff --check`

Expected: 无输出，退出码 0。

- [ ] **Step 5: 提交验收与文档**

```bash
git add apps/web/e2e/operations-security.spec.ts apps/web/e2e/fixtures.ts deploy/README.md deploy/PRODUCTION.md
git commit -m "test: 覆盖管理员改密验收流程"
```

### Plan 1 完成检查点

- [ ] `POST /api/auth/password` 的权限、同源、JSON、限速和 no-store 测试全部通过。
- [ ] 密码、会话撤销与审计事务集成测试通过。
- [ ] `/operations` 账号安全界面和 E2E 通过。
- [ ] 尚未删除生产初始凭据文件；只有部署并重新登录验证成功后才执行删除。
- [ ] 工作区干净，每个任务都有独立提交。
