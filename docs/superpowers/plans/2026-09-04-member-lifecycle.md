# 云令团队成员生命周期 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在全中文控制台中补齐成员创建、筛选、启停、软删除、恢复、角色调整、管理员重置密码和首次登录强制改密。

**Architecture:** 在 `users` 表增加首次改密与软删除状态，由 `auth.TeamService` 负责输入校验和临时密码生成，由 PostgreSQL 仓储在单个事务中完成成员状态、角色、会话撤销和审计写入。HTTP 层扩展 `/api/members`，React 端用聚焦的对话框组件扩展现有成员页，并通过会话门禁把临时密码用户引导到独立改密页。

**Tech Stack:** Go 1.25、PostgreSQL/pgx、React 19、TypeScript 7、React Router 7、Vitest/Testing Library、Playwright。

**Spec:** `docs/superpowers/specs/2026-09-04-member-lifecycle-design.md`

## Global Constraints

- 首版不接入邮件服务，也不发送邮件邀请。
- 删除必须是软删除，历史用户 ID 和关联记录不得删除。
- 临时密码只在创建或重置成功响应中出现一次，禁止写入应用日志和审计详情。
- 停用、移除和重置密码必须在同一数据库事务中撤销目标成员全部现有会话。
- 不能操作当前管理员本人，不能让系统失去最后一名未移除、已启用且拥有 `admin` 角色的成员。
- 现有账号迁移后默认 `must_change_password=false`、`removed_at=null`。
- 所有新增界面、接口错误和测试描述使用中文。
- 不增加邮件、SSO、自定义角色或新的运行时依赖。

---

### Task 1: 成员生命周期数据库状态

**Files:**
- Create: `migrations/000013_member_lifecycle.up.sql`
- Create: `migrations/000013_member_lifecycle.down.sql`
- Modify: `internal/store/postgres/migrations_test.go`

**Interfaces:**
- Produces: `users.must_change_password boolean NOT NULL DEFAULT false`
- Produces: `users.removed_at timestamptz NULL`
- Produces: `users_removed_at_idx`
- Produces: `schema_migrations.version = 13`

- [ ] **Step 1: Write the failing migration test**

Add a test that applies every migration, inspects the two columns and verifies defaults for an existing-style user:

```go
func TestMemberLifecycleMigrationAddsCompatibleUserState(t *testing.T) {
	db := startPostgres(t)
	applyMigrations(t, db)
	ctx := context.Background()

	var mustChange bool
	var removedAt *time.Time
	err := db.QueryRow(ctx, `
		INSERT INTO users (email, display_name, password_hash)
		VALUES ('existing@example.com', '现有成员', 'hash')
		RETURNING must_change_password, removed_at
	`).Scan(&mustChange, &removedAt)
	if err != nil { t.Fatal(err) }
	if mustChange || removedAt != nil {
		t.Fatalf("现有账号默认值错误：mustChange=%v removedAt=%v", mustChange, removedAt)
	}
	if !tableIndexExists(t, db, "users_removed_at_idx") {
		t.Fatal("成员软删除筛选索引不存在")
	}
}
```

Add `tableIndexExists` beside `tableExists`, using `SELECT to_regclass('public.' || $1) IS NOT NULL`.

- [ ] **Step 2: Run the test and verify it fails**

Run: `go test ./internal/store/postgres -run TestMemberLifecycleMigrationAddsCompatibleUserState -count=1`

Expected: FAIL because `must_change_password` and `removed_at` do not exist.

- [ ] **Step 3: Add the up and down migrations**

Use this up migration:

```sql
ALTER TABLE users
    ADD COLUMN must_change_password boolean NOT NULL DEFAULT false,
    ADD COLUMN removed_at timestamptz;

CREATE INDEX users_removed_at_idx ON users (removed_at, created_at, id);

INSERT INTO schema_migrations (version) VALUES (13)
ON CONFLICT (version) DO NOTHING;
```

Use this down migration:

```sql
DELETE FROM schema_migrations WHERE version = 13;
DROP INDEX IF EXISTS users_removed_at_idx;
ALTER TABLE users DROP COLUMN IF EXISTS removed_at;
ALTER TABLE users DROP COLUMN IF EXISTS must_change_password;
```

- [ ] **Step 4: Run migration tests**

Run: `go test ./internal/store/postgres -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add migrations/000013_member_lifecycle.* internal/store/postgres/migrations_test.go
git commit -m "feat: 增加成员生命周期数据状态"
```

---

### Task 2: 成员领域模型与服务校验

**Files:**
- Modify: `internal/auth/team.go`
- Modify: `internal/auth/team_test.go`
- Modify: `internal/auth/model.go`

**Interfaces:**
- Produces: `type MemberStatus string` with `MemberStatusActive`, `MemberStatusDisabled`, `MemberStatusRemoved`, `MemberStatusAll`
- Produces: `type CreateMemberInput struct { Email, DisplayName string; Roles []RoleName }`
- Produces: `type CreateMemberResult struct { Member Member; TemporaryPassword string }`
- Produces: `type ResetPasswordResult struct { Member Member; TemporaryPassword string }`
- Produces: `TeamService.List(ctx context.Context, status MemberStatus) ([]Member, error)`
- Produces: `TeamService.Create(ctx context.Context, actorID string, input CreateMemberInput) (CreateMemberResult, error)`
- Produces: `TeamService.UpdateRoles(ctx context.Context, actorID, userID string, roles []RoleName) (Member, error)`
- Produces: `TeamService.SetEnabled(ctx context.Context, actorID, userID string, enabled bool) (Member, error)`
- Produces: `TeamService.Remove(ctx context.Context, actorID, userID string) (Member, error)`
- Produces: `TeamService.Restore(ctx context.Context, actorID, userID string) (Member, error)`
- Produces: `TeamService.ResetPassword(ctx context.Context, actorID, userID string) (ResetPasswordResult, error)`

Give `CreateMemberResult` and `ResetPasswordResult` JSON fields the tags `member` and `temporaryPassword`. Define `MemberStatusAll` as all users whose `removed_at` is null; removed users are only returned by `MemberStatusRemoved`.

- [ ] **Step 1: Write failing service tests**

Replace the memory repository with one that records calls, then cover normalization, password hashing and self-protection:

```go
func TestTeamServiceCreatesMemberWithNormalizedInputAndTemporaryPassword(t *testing.T) {
	repository := &memoryTeamRepository{}
	service := auth.NewTeamService(repository)
	result, err := service.Create(context.Background(), "admin-1", auth.CreateMemberInput{
		Email: " OPS@Example.COM ", DisplayName: " 值班运维 ",
		Roles: []auth.RoleName{auth.RoleViewer, auth.RoleOperator, auth.RoleViewer},
	})
	if err != nil { t.Fatal(err) }
	if repository.created.Email != "ops@example.com" || repository.created.DisplayName != "值班运维" {
		t.Fatalf("创建输入未规范化：%+v", repository.created)
	}
	if len(result.TemporaryPassword) < 12 { t.Fatal("临时密码长度不足") }
	valid, err := auth.VerifyPassword(repository.created.PasswordHash, result.TemporaryPassword)
	if err != nil || !valid { t.Fatal("临时密码没有以哈希形式传入仓储") }
}

func TestTeamServiceRejectsSelfMutation(t *testing.T) {
	service := auth.NewTeamService(&memoryTeamRepository{})
	if _, err := service.SetEnabled(context.Background(), "admin-1", "admin-1", false); !errors.Is(err, auth.ErrCannotModifySelf) {
		t.Fatalf("管理员必须不能停用自己：%v", err)
	}
}
```

Also test invalid email, blank display name, empty/unknown roles, invalid status filter, remove/restore delegation and reset-password hashing.

- [ ] **Step 2: Run the tests and verify they fail**

Run: `go test ./internal/auth -run 'TestTeamService' -count=1`

Expected: FAIL because lifecycle types and methods are undefined.

- [ ] **Step 3: Implement the domain types and service**

Extend `Member` with JSON fields:

```go
MustChangePassword bool       `json:"mustChangePassword"`
RemovedAt          *time.Time `json:"removedAt"`
```

Add the explicit domain errors `ErrInvalidMember`, `ErrDuplicateEmail`, `ErrMemberStateConflict`, `ErrCannotModifySelf`, and `ErrLastAdmin`. Normalize roles through one `normalizeRoles` helper shared by create and update. Generate 18 random bytes through the existing cryptographic token helper, hash with `HashPassword`, and only return plaintext in `CreateMemberResult` or `ResetPasswordResult`.

Define repository records and methods exactly as follows:

```go
type CreateMemberRecord struct {
	Email, DisplayName, PasswordHash string
	Roles []RoleName
}

type TeamRepository interface {
	ListMembers(context.Context, MemberStatus) ([]Member, error)
	CreateMember(context.Context, string, CreateMemberRecord) (Member, error)
	ReplaceMemberRoles(context.Context, string, string, []RoleName) (Member, error)
	SetMemberEnabled(context.Context, string, string, bool) (Member, error)
	RemoveMember(context.Context, string, string) (Member, error)
	RestoreMember(context.Context, string, string) (Member, error)
	ResetMemberPassword(context.Context, string, string, string) (Member, error)
}
```

The first string on mutation methods is `actorID`; the second is `targetID`.

- [ ] **Step 4: Run focused service tests**

Run: `go test ./internal/auth -run 'TestTeamService' -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/auth/team.go internal/auth/team_test.go internal/auth/model.go
git commit -m "feat: 定义成员生命周期服务"
```

---

### Task 3: PostgreSQL 成员事务与审计

**Files:**
- Create: `internal/auth/team_postgres.go`
- Create: `internal/auth/team_postgres_test.go`
- Modify: `internal/auth/postgres.go`
- Modify: `internal/auth/postgres_test.go`

**Interfaces:**
- Consumes: Task 1 user columns and Task 2 `TeamRepository`
- Produces: complete `TeamRepository` implementation on `PostgresRepository`
- Produces: audit actions `member.create`, `member.enable`, `member.disable`, `member.remove`, `member.restore`, `member.roles.update`, `member.password.reset`

- [ ] **Step 1: Write failing PostgreSQL integration tests**

Apply migrations `000001_initial.up.sql`, `000010_password_change_security.up.sql`, and `000013_member_lifecycle.up.sql`. Seed two administrators and one viewer, then test each transaction. The most important assertions are:

```go
func TestPostgresMemberDisableRevokesSessionsAndAuditsAtomically(t *testing.T) {
	db, actorID, targetID := memberDatabase(t)
	repository := auth.NewPostgresRepository(db)
	member, err := repository.SetMemberEnabled(context.Background(), actorID, targetID, false)
	if err != nil { t.Fatal(err) }
	if member.Enabled { t.Fatal("成员仍处于启用状态") }

	var liveSessions, audits int
	_ = db.QueryRow(context.Background(), `SELECT count(*) FROM sessions WHERE user_id=$1 AND revoked_at IS NULL`, targetID).Scan(&liveSessions)
	_ = db.QueryRow(context.Background(), `SELECT count(*) FROM audit_logs WHERE actor_id=$1 AND target_id=$2 AND action='member.disable'`, actorID, targetID).Scan(&audits)
	if liveSessions != 0 || audits != 1 { t.Fatalf("事务结果错误：sessions=%d audits=%d", liveSessions, audits) }
}

func TestPostgresMemberMutationPreservesLastAdmin(t *testing.T) {
	db, actorID := singleAdminDatabase(t)
	repository := auth.NewPostgresRepository(db)
	_, err := repository.RemoveMember(context.Background(), "other-admin-id", actorID)
	if !errors.Is(err, auth.ErrLastAdmin) { t.Fatalf("最后管理员应被保护：%v", err) }
}
```

Also assert active/disabled/removed/all filters, duplicate email mapping, restore remaining disabled, role replacement protection, password reset session revocation, and every audit detail excluding hashes and plaintext.

Use these concrete database helpers in the same test file:

```go
func memberDatabase(t *testing.T) (*pgxpool.Pool, string, string) {
	t.Helper()
	db := testpostgres.Start(t)
	testpostgres.ApplyInitialMigration(t, db)
	testpostgres.ApplyMigration(t, db, "000010_password_change_security.up.sql")
	testpostgres.ApplyMigration(t, db, "000013_member_lifecycle.up.sql")
	ctx := context.Background()
	hash, err := auth.HashPassword("member-password-2026")
	if err != nil { t.Fatal(err) }
	if _, err := db.Exec(ctx, `INSERT INTO roles (name, permissions) VALUES ('admin','[]'),('viewer','[]') ON CONFLICT (name) DO NOTHING`); err != nil { t.Fatal(err) }
	var actorID, targetID string
	if err := db.QueryRow(ctx, `INSERT INTO users (email,display_name,password_hash) VALUES ('admin@example.com','管理员',$1) RETURNING id::text`, hash).Scan(&actorID); err != nil { t.Fatal(err) }
	if err := db.QueryRow(ctx, `INSERT INTO users (email,display_name,password_hash) VALUES ('viewer@example.com','只读成员',$1) RETURNING id::text`, hash).Scan(&targetID); err != nil { t.Fatal(err) }
	if _, err := db.Exec(ctx, `INSERT INTO user_roles (user_id,role_id) SELECT $1,id FROM roles WHERE name='admin' UNION ALL SELECT $2,id FROM roles WHERE name='viewer'`, actorID, targetID); err != nil { t.Fatal(err) }
	sessionHash := sha256.Sum256([]byte("target-session"))
	if _, err := db.Exec(ctx, `INSERT INTO sessions (user_id,token_hash,expires_at) VALUES ($1,$2,now()+interval '1 hour')`, targetID, sessionHash[:]); err != nil { t.Fatal(err) }
	return db, actorID, targetID
}

func singleAdminDatabase(t *testing.T) (*pgxpool.Pool, string) {
	db, actorID, targetID := memberDatabase(t)
	if _, err := db.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, targetID); err != nil { t.Fatal(err) }
	return db, actorID
}
```

- [ ] **Step 2: Run the PostgreSQL tests and verify they fail**

Run: `go test ./internal/auth -run 'TestPostgresMember' -count=1`

Expected: FAIL because the new repository methods do not exist.

- [ ] **Step 3: Move existing team queries into the focused repository file**

Move `ListMembers` and `ReplaceMemberRoles` out of `postgres.go` into `team_postgres.go`, update their signatures, and add `CreateMember`, `SetMemberEnabled`, `RemoveMember`, `RestoreMember`, and `ResetMemberPassword`.

Every mutation must:

1. begin a pgx transaction;
2. lock the target user with `FOR UPDATE` where applicable;
3. reject removed/state-conflicting targets;
4. acquire `pg_advisory_xact_lock(hashtext('yunling-member-admin'))` before checking the effective administrator count;
5. modify the user/roles;
6. revoke sessions for disable, remove, and password reset;
7. insert the matching `audit_logs` row without password data;
8. load the complete member projection;
9. commit.

Use a shared projection that scans:

```sql
u.id::text, u.email, u.display_name, u.enabled,
u.must_change_password, u.removed_at, u.created_at,
COALESCE(array_agg(role.name ORDER BY role.name)
  FILTER (WHERE role.name IS NOT NULL), ARRAY[]::text[])
```

Map PostgreSQL unique violation `23505` on `users_email_unique` to `ErrDuplicateEmail`.

- [ ] **Step 4: Run all auth tests**

Run: `go test ./internal/auth -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/auth/team_postgres.go internal/auth/team_postgres_test.go internal/auth/postgres.go internal/auth/postgres_test.go
git commit -m "feat: 持久化成员生命周期事务"
```

---

### Task 4: 成员管理 HTTP API

**Files:**
- Modify: `internal/securityhttp/handler.go`
- Modify: `internal/securityhttp/handler_test.go`
- Modify: `cmd/api/main.go`

**Interfaces:**
- Consumes: Task 2 `TeamService` lifecycle methods
- Produces: all `/api/members` endpoints from the approved spec

- [ ] **Step 1: Write failing handler tests**

Extend `fakeTeam` to implement the lifecycle interface. Add table-driven coverage for status filtering and all mutations. Include this one-time-secret assertion:

```go
func TestMemberCreateReturnsTemporaryPasswordOnceWithoutAuditEcho(t *testing.T) {
	team := &fakeTeam{createResult: auth.CreateMemberResult{
		Member: auth.Member{ID: "user-2", Email: "ops@example.com"},
		TemporaryPassword: "temporary-password",
	}}
	handler := securityhttp.NewHandler(securityhttp.Services{Team: team})
	request := adminRequest(http.MethodPost, "/api/members", `{"email":"ops@example.com","displayName":"值班运维","roles":["operator"]}`)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("创建响应错误：code=%d headers=%v", recorder.Code, recorder.Header())
	}
	if !strings.Contains(recorder.Body.String(), "temporary-password") { t.Fatal("没有返回一次性临时密码") }
}
```

Define the request helper in `handler_test.go`:

```go
func adminRequest(method, target, body string) *http.Request {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	return request.WithContext(auth.WithPrincipal(request.Context(), auth.Principal{
		UserID: "admin-1", Roles: []auth.RoleName{auth.RoleAdmin},
	}))
}
```

Test `403` for non-admin writes, `400` invalid input, `404` missing member, `409` duplicate/state/self/last-admin errors, and `204` only where the endpoint has no response body.

- [ ] **Step 2: Run handler tests and verify they fail**

Run: `go test ./internal/securityhttp -count=1`

Expected: FAIL because `TeamManager` and routes only support listing and role updates.

- [ ] **Step 3: Implement routes and error mapping**

Expand `TeamManager` to match Task 2 service methods. Register:

```go
router.Handle("GET /api/members", auth.Require(auth.PermissionRead)(listMembers(services.Team)))
router.Handle("POST /api/members", auth.Require(auth.PermissionAdmin)(createMember(services.Team)))
router.Handle("PUT /api/members/{id}/roles", auth.Require(auth.PermissionAdmin)(updateMemberRoles(services.Team)))
router.Handle("POST /api/members/{id}/enable", auth.Require(auth.PermissionAdmin)(setMemberEnabled(services.Team, true)))
router.Handle("POST /api/members/{id}/disable", auth.Require(auth.PermissionAdmin)(setMemberEnabled(services.Team, false)))
router.Handle("DELETE /api/members/{id}", auth.Require(auth.PermissionAdmin)(removeMember(services.Team)))
router.Handle("POST /api/members/{id}/restore", auth.Require(auth.PermissionAdmin)(restoreMember(services.Team)))
router.Handle("POST /api/members/{id}/password/reset", auth.Require(auth.PermissionAdmin)(resetMemberPassword(services.Team)))
```

Read `actorID` from `auth.PrincipalFromContext`. Set `Cache-Control: no-store` on create and reset responses. Do not call the existing separate `recordAudit` helper for member mutations because the Task 3 transaction already records them.

- [ ] **Step 4: Run handler and API package tests**

Run: `go test ./internal/securityhttp ./cmd/api -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/securityhttp/handler.go internal/securityhttp/handler_test.go cmd/api/main.go
git commit -m "feat: 提供成员生命周期接口"
```

---

### Task 5: 首次登录强制改密后端门禁

**Files:**
- Modify: `internal/auth/model.go`
- Modify: `internal/auth/postgres.go`
- Modify: `internal/auth/session.go`
- Modify: `internal/auth/http.go`
- Modify: `internal/auth/middleware.go`
- Modify: `internal/auth/password_change_postgres.go`
- Modify: `internal/auth/service_test.go`
- Modify: `internal/auth/http_test.go`
- Modify: `internal/auth/password_http_test.go`
- Modify: `internal/auth/password_change_postgres_test.go`

**Interfaces:**
- Produces: `User.MustChangePassword bool`
- Produces: `Session.MustChangePassword bool`
- Produces: `Principal.MustChangePassword bool` serialized as `must_change_password`
- Produces: authenticated API response `403 {"message":"请先修改临时密码","code":"password_change_required"}`

- [ ] **Step 1: Write failing authentication tests**

Cover login/session propagation, removed-user rejection and the forced-change gate:

```go
func TestAuthenticateAllowsOnlyPasswordChangeWhenRequired(t *testing.T) {
	service := authenticatedService(auth.Principal{UserID: "user-1", MustChangePassword: true})
	protected := auth.Authenticate(service)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))

	blocked := requestWithSession(http.MethodGet, "/api/members")
	blockedRecorder := httptest.NewRecorder()
	protected.ServeHTTP(blockedRecorder, blocked)
	if blockedRecorder.Code != http.StatusForbidden || !strings.Contains(blockedRecorder.Body.String(), "password_change_required") {
		t.Fatalf("临时密码会话未被门禁：%d %s", blockedRecorder.Code, blockedRecorder.Body.String())
	}

	allowed := requestWithSession(http.MethodPost, "/api/auth/password")
	allowedRecorder := httptest.NewRecorder()
	protected.ServeHTTP(allowedRecorder, allowed)
	if allowedRecorder.Code != http.StatusNoContent { t.Fatalf("改密接口被错误阻止：%d", allowedRecorder.Code) }
}
```

Define the concrete repository and request helpers in the same test file:

```go
type requiredPasswordRepository struct{ principal auth.Principal }

func (r *requiredPasswordRepository) FindByEmail(context.Context, string) (auth.User, error) {
	return auth.User{}, auth.ErrUserNotFound
}
func (r *requiredPasswordRepository) Create(context.Context, auth.StoredSession) error { return nil }
func (r *requiredPasswordRepository) FindPrincipal(context.Context, []byte) (auth.Principal, error) {
	return r.principal, nil
}
func (r *requiredPasswordRepository) Revoke(context.Context, []byte) error { return nil }

func authenticatedService(principal auth.Principal) *auth.Service {
	repository := &requiredPasswordRepository{principal: principal}
	return auth.NewService(repository, repository)
}

func requestWithSession(method, target string) *http.Request {
	request := httptest.NewRequest(method, target, nil)
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session-token"})
	return request
}
```

Add a PostgreSQL test proving `CommitPasswordChange` atomically sets `must_change_password=false` while keeping the current session and revoking all other sessions.

- [ ] **Step 2: Run focused auth tests and verify they fail**

Run: `go test ./internal/auth -run 'Test.*(PasswordChangeRequired|MustChange|RemovedUser)' -count=1`

Expected: FAIL because the state is not loaded or enforced.

- [ ] **Step 3: Propagate and enforce the state**

Update `FindByEmail` to select `must_change_password` and require `removed_at IS NULL`. Update `FindPrincipal` to select the flag and require `u.removed_at IS NULL`. Carry the flag through `Login`, session output and `GET /api/auth/session`.

In `Authenticate`, after storing the principal in context, reject required-change requests unless `r.URL.Path == "/api/auth/password"`. Return both the Chinese message and stable code through a new helper:

```go
func writeAuthCodeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "message": message})
}
```

Change the password commit statement to:

```sql
UPDATE users
SET password_hash=$2, must_change_password=false, updated_at=$3
WHERE id=$1
```

- [ ] **Step 4: Run all auth tests**

Run: `go test ./internal/auth -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/auth/model.go internal/auth/postgres.go internal/auth/session.go internal/auth/http.go internal/auth/middleware.go internal/auth/password_change_postgres.go internal/auth/*test.go
git commit -m "feat: 强制临时密码用户首次改密"
```

---

### Task 6: 前端成员 API 契约

**Files:**
- Modify: `apps/web/src/api/client.ts`
- Modify: `apps/web/src/api/client.test.ts`

**Interfaces:**
- Produces: `Member.removedAt: string | null`
- Produces: `Member.mustChangePassword: boolean`
- Produces: `SessionUser.mustChangePassword: boolean`
- Produces: `createMember`, `setMemberEnabled`, `removeMember`, `restoreMember`, `resetMemberPassword`

- [ ] **Step 1: Write failing client tests**

Add request-contract tests that assert method, URL, body and returned temporary password:

```ts
it('创建成员并读取一次性临时密码', async () => {
  vi.stubGlobal('fetch', vi.fn(async () => response({
    member: memberFixture,
    temporaryPassword: 'temporary-password',
  })))
  const result = await createMember({ displayName: '值班运维', email: 'ops@example.com', roles: ['operator'] })
  expect(result.temporaryPassword).toBe('temporary-password')
  expect(fetch).toHaveBeenCalledWith('/api/members', expect.objectContaining({ method: 'POST' }))
})
```

Also test query encoding for `getMembers('removed')`, enable/disable POST paths, DELETE remove, restore POST, reset POST, and snake-case session mapping.

- [ ] **Step 2: Run client tests and verify they fail**

Run: `npm --workspace apps/web test -- --run src/api/client.test.ts`

Expected: FAIL because the functions and fields are absent.

- [ ] **Step 3: Implement the typed client**

Add:

```ts
export type MemberStatus = 'active' | 'disabled' | 'removed' | 'all'
export interface TemporaryPasswordResult { member: Member; temporaryPassword: string }
export interface CreateMemberInput { displayName: string; email: string; roles: RoleName[] }

export async function getMembers(status: MemberStatus = 'all'): Promise<Member[]> {
  const response = await request<{ members: Member[] }>(`/api/members?status=${encodeURIComponent(status)}`)
  return response.members
}
```

Implement the remaining mutation functions with the exact paths in Task 4. Map `must_change_password` from `/api/auth/session` to `mustChangePassword`.

- [ ] **Step 4: Run client tests**

Run: `npm --workspace apps/web test -- --run src/api/client.test.ts`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add apps/web/src/api/client.ts apps/web/src/api/client.test.ts
git commit -m "feat: 扩展成员管理前端接口"
```

---

### Task 7: 团队成员完整管理界面

**Files:**
- Modify: `apps/web/src/features/settings/MembersPage.tsx`
- Modify: `apps/web/src/features/settings/MembersPage.test.tsx`
- Create: `apps/web/src/features/settings/MemberFormDialog.tsx`
- Create: `apps/web/src/features/settings/MemberActionDialog.tsx`
- Create: `apps/web/src/features/settings/TemporaryPasswordDialog.tsx`
- Modify: `apps/web/src/app/styles.css`

**Interfaces:**
- Consumes: Task 6 member client functions
- Produces: admin create/filter/enable/disable/remove/restore/reset UI
- Produces: one-time password dialog with `onClose(): void`

- [ ] **Step 1: Write failing component tests**

Expand `MembersPage.test.tsx` with separate tests for create, filtering, lifecycle actions and non-admin visibility. The create test must verify the complete user path:

```tsx
it('管理员创建成员后只显示一次临时密码并可复制', async () => {
  const user = userEvent.setup()
  stubMemberApi({ temporaryPassword: 'temporary-password' })
  render(<MembersPage />)
  await user.click(await screen.findByRole('button', { name: '创建成员' }))
  await user.type(screen.getByLabelText('姓名'), '值班运维')
  await user.type(screen.getByLabelText('邮箱'), 'ops@example.com')
  await user.click(screen.getByRole('checkbox', { name: '运维人员' }))
  await user.click(screen.getByRole('button', { name: '确认创建' }))
  expect(await screen.findByRole('dialog', { name: '一次性临时密码' })).toHaveTextContent('temporary-password')
  await user.click(screen.getByRole('button', { name: '我已保存，关闭' }))
  expect(screen.queryByText('temporary-password')).not.toBeInTheDocument()
})
```

Define `stubMemberApi` in the test file so every request is deterministic:

```tsx
function stubMemberApi(options: { temporaryPassword: string }) {
  const member = {
    id: 'user-2', email: 'ops@example.com', displayName: '值班运维', enabled: true,
    mustChangePassword: true, removedAt: null, roles: ['operator'], createdAt: '2026-09-04T00:00:00Z',
  }
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input)
    if (path === '/api/auth/session') return response({ user: {
      user_id: 'admin-1', email: 'admin@example.com', display_name: '管理员',
      roles: ['admin'], must_change_password: false,
    } })
    if (path === '/api/members' && init?.method === 'POST') {
      return response({ member, temporaryPassword: options.temporaryPassword }, 201)
    }
    if (path.startsWith('/api/members?')) return response({ members: [] })
    return response(member)
  }))
}
```

Test that disable/remove/reset confirmations explain session invalidation, restore keeps the member disabled, filters call the correct status, self actions are hidden/disabled, and a viewer sees no mutation controls.

- [ ] **Step 2: Run member-page tests and verify they fail**

Run: `npm --workspace apps/web test -- --run src/features/settings/MembersPage.test.tsx`

Expected: FAIL because only role editing exists.

- [ ] **Step 3: Build focused dialogs and refactor the page**

`MemberFormDialog` owns name/email/roles, validates required fields and calls `onSubmit(input)`. `MemberActionDialog` receives `{ member, action, onConfirm, onClose }` and renders exact Chinese impact text. `TemporaryPasswordDialog` keeps the password only in component state, copies with `navigator.clipboard.writeText`, clears state on close, and restores focus to the triggering control.

Keep `MembersPage` responsible for loading, filtering and choosing the active dialog. Its page heading must include:

```tsx
<button className="primary-action" type="button" onClick={() => setDialog({ kind: 'create' })}>
  创建成员
</button>
```

Render filter buttons with `aria-pressed`, keep the existing roles explanation, and use a compact row action menu on desktop and mobile. Extend `styles.css` with existing color variables and a visible `:focus-visible` state; do not introduce a new CSS framework.

- [ ] **Step 4: Run member tests and the full web suite**

Run: `npm --workspace apps/web test -- --run src/features/settings/MembersPage.test.tsx`

Expected: PASS.

Run: `npm run test:web`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add apps/web/src/features/settings apps/web/src/app/styles.css
git commit -m "feat: 完成团队成员管理界面"
```

---

### Task 8: 临时密码强制改密界面

**Files:**
- Create: `apps/web/src/features/auth/RequiredPasswordPage.tsx`
- Create: `apps/web/src/features/auth/RequiredPasswordPage.test.tsx`
- Modify: `apps/web/src/features/auth/LoginPage.tsx`
- Modify: `apps/web/src/features/auth/LoginPage.test.tsx`
- Modify: `apps/web/src/features/operations/AccountSecurityPanel.tsx`
- Modify: `apps/web/src/features/operations/AccountSecurityPanel.test.tsx`
- Modify: `apps/web/src/app/App.tsx`
- Modify: `apps/web/src/app/App.test.tsx`
- Modify: `apps/web/src/app/styles.css`

**Interfaces:**
- Consumes: `SessionUser.mustChangePassword` from Task 6
- Produces: `/password-required` route
- Produces: `AccountSecurityPanel` props `{ required?: boolean; onChanged?: () => void }`

- [ ] **Step 1: Write failing route and page tests**

Test that login honors the response flag, the console gate redirects a required-change session, and successful change enters the dashboard:

```tsx
it('临时密码用户完成改密后进入运行总览', async () => {
  vi.mocked(getSession).mockResolvedValue({
    id: 'user-2', email: 'ops@example.com', displayName: '值班运维',
    roles: ['operator'], mustChangePassword: true,
  })
  vi.mocked(changePassword).mockResolvedValue(undefined)
  window.history.pushState({}, '', '/')
  render(<App />)
  expect(await screen.findByRole('heading', { name: '首次登录，请修改密码' })).toBeVisible()
  await fillAndSubmitPasswordForm(userEvent.setup())
  expect(await screen.findByRole('heading', { name: '运行总览' })).toBeVisible()
})
```

Define the form helper immediately below the test:

```tsx
async function fillAndSubmitPasswordForm(user: ReturnType<typeof userEvent.setup>) {
  await user.type(screen.getByLabelText('当前密码'), 'temporary-password')
  await user.type(screen.getByLabelText('新密码'), 'member-password-2026')
  await user.type(screen.getByLabelText('确认新密码'), 'member-password-2026')
  await user.click(screen.getByRole('button', { name: '更新密码' }))
}
```

Also verify a normal session never sees the required page and manually opening `/password-required` with a normal session redirects to `/`.

- [ ] **Step 2: Run focused tests and verify they fail**

Run: `npm --workspace apps/web test -- --run src/features/auth/RequiredPasswordPage.test.tsx src/features/auth/LoginPage.test.tsx src/app/App.test.tsx`

Expected: FAIL because the required route and gate are missing.

- [ ] **Step 3: Implement the session gate and required page**

Make login parse `{ must_change_password: boolean }` and choose `/password-required` or `/`. Add a `ConsoleAccessGate` that loads `getSession`; while loading it renders a Chinese progress state, redirects unauthenticated users to `/login`, and renders `RequiredPasswordPage` whenever the flag is true.

Reuse `AccountSecurityPanel` with `required` to change the title and explanatory copy. On success call `onChanged`; the required page then refreshes session state and navigates to `/`. The account panel must still preserve its current behavior and tests in normal mode.

- [ ] **Step 4: Run focused tests, full web tests and build**

Run: `npm --workspace apps/web test -- --run src/features/auth src/features/operations/AccountSecurityPanel.test.tsx src/app/App.test.tsx`

Expected: PASS.

Run: `npm run test:web`

Expected: PASS.

Run: `npm run build:web`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add apps/web/src/features/auth apps/web/src/features/operations/AccountSecurityPanel* apps/web/src/app/App* apps/web/src/app/styles.css
git commit -m "feat: 引导临时密码成员首次改密"
```

---

### Task 9: 端到端流程与完整验证

**Files:**
- Create: `apps/web/e2e/member-lifecycle.spec.ts`
- Modify: `apps/web/e2e/fixtures.ts`
- Modify: `README.md`

**Interfaces:**
- Consumes: Tasks 1–8 complete lifecycle
- Produces: browser-level regression coverage and Chinese operator documentation

- [ ] **Step 1: Add the failing Playwright scenario**

Extend fixtures with stateful mocked `/api/members` and auth/session responses. The scenario must:

```ts
test('管理员创建成员并完成首次改密流程', async ({ page }) => {
  await page.goto('/members')
  await page.getByRole('button', { name: '创建成员' }).click()
  await page.getByLabel('姓名').fill('值班运维')
  await page.getByLabel('邮箱').fill('ops@example.com')
  await page.getByRole('checkbox', { name: '运维人员' }).check()
  await page.getByRole('button', { name: '确认创建' }).click()
  await expect(page.getByRole('dialog', { name: '一次性临时密码' })).toBeVisible()
  await page.getByRole('button', { name: '我已保存，关闭' }).click()
  await page.goto('/password-required')
  await expect(page.getByRole('heading', { name: '首次登录，请修改密码' })).toBeVisible()
})
```

Add separate assertions for disable, removed filter, restore and password reset confirmation.

- [ ] **Step 2: Run the new E2E test and verify it fails before fixtures are complete**

Run: `npm --workspace apps/web run test:e2e -- member-lifecycle.spec.ts`

Expected: FAIL on the first missing mocked lifecycle response.

- [ ] **Step 3: Complete stateful fixtures and user documentation**

Implement fixture state transitions matching the HTTP contract. Add a README section “团队成员管理” documenting: direct account creation, one-time password handling, enabled/disabled/removed differences, restore-then-enable, reset impact, and first-login password change.

- [ ] **Step 4: Run complete verification**

Run locally where tools exist:

```bash
go test -race -p=1 ./... -count=1
npm run test:web
npm run build:web
npm run test:e2e
git diff --check
```

Expected: all commands PASS and `git diff --check` prints no output.

Because the Windows host may not have Go, run the same backend command in the repository CI and require the “后端测试与构建” job to pass before merging.

- [ ] **Step 5: Commit**

```bash
git add apps/web/e2e/member-lifecycle.spec.ts apps/web/e2e/fixtures.ts README.md
git commit -m "test: 覆盖成员完整生命周期"
```

---

### Task 10: Final review and delivery readiness

**Files:**
- Review: `docs/superpowers/specs/2026-09-04-member-lifecycle-design.md`
- Review: all files changed in Tasks 1–9

**Interfaces:**
- Produces: a clean feature branch ready for code review and Pull Request

- [ ] **Step 1: Verify spec coverage**

Check every section of the design spec against code and automated tests. Confirm direct creation, no email dependency, soft deletion, restore, session revocation, audit, last-admin protection, one-time display, forced change and Chinese copy each have at least one test.

- [ ] **Step 2: Inspect the complete diff**

Run:

```bash
git status --short
git diff origin/main...HEAD --stat
git diff origin/main...HEAD --check
```

Expected: only intended member-lifecycle files are changed and the check has no output.

- [ ] **Step 3: Run the verification commands again from a clean checkout**

Run the same five commands from Task 9 Step 4. Record exact test counts and build result in the handoff.

- [ ] **Step 4: Request code review**

Use `superpowers:requesting-code-review`, resolve findings with focused tests, and do not merge or deploy without the user's separate confirmation.

- [ ] **Step 5: Prepare the Pull Request summary**

Summarize the functional outcome, database migration, API endpoints, UI workflow, verification evidence and deployment impact. Explicitly state that production has not been changed.
