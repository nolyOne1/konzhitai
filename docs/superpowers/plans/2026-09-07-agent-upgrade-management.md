# 云令代理版本与分批升级管理 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为云令增加多版本代理仓库、服务器版本盘点、无需 SSH 的代理自升级、首批单节点验证、自动分批推进、失败暂停及失败批次回滚。

**Architecture:** 控制面以 PostgreSQL 持久化版本、计划、目标与事件，通过现有 WebSocket 向代理发送幂等升级指令；代理下载不可变安装包并调用受限 systemd 一次性升级单元完成原子替换和本机回滚。React 控制台在服务器模块内提供版本盘点、三步升级向导、实时计划详情和历史记录。

**Tech Stack:** Go 1.27、PostgreSQL 18、MinIO S3、coder/websocket、React 19、TypeScript 7、Vitest、Playwright、Bash、systemd、polkit

**Spec:** `docs/superpowers/specs/2026-09-05-agent-upgrade-management-design.md`

## Global Constraints

- 首期只支持 Linux amd64 与 Linux arm64，并要求 systemd 240 或更高版本。
- 首批固定为 1 台；后续批次大小默认 1 台且由管理员创建计划时设置。
- 升级前自动排空；不得强制终止正在运行的任务。
- 任一节点失败立即暂停计划，并自动回滚失败批次；此前成功批次保持目标版本。
- 同一时间只允许运行一个升级计划。
- 版本与对象路径不可变；已发布版本不得覆盖。
- 旧代理没有 `self_upgrade_v1` 能力时只能盘点，不能加入升级计划。
- 面向用户的页面、状态、错误与日志摘要全部使用中文。
- 不在浏览器内编译或上传任意代理二进制，不保存服务器 SSH 密码。
- 每个任务严格按测试先行执行，并在本地提交后再开始下一任务。

---

## File Structure

### Database and control plane

- `migrations/000014_agent_upgrade_management.up.sql`：新增版本、产物、计划、目标、事件和服务器能力字段。
- `migrations/000014_agent_upgrade_management.down.sql`：只回退本迁移新增对象。
- `internal/agentrelease/model.go`：多版本发布模型与输入校验。
- `internal/agentrelease/postgres.go`：代理版本元数据仓库。
- `internal/agentrelease/service.go`：不可变导入、推荐版本、查询与下载定位。
- `internal/agentrelease/management_http.go`：登录用户的版本列表与管理员推荐接口。
- `internal/agentrelease/http.go`：公开推荐清单和不可变安装包下载。
- `cmd/agent-release/main.go`：运维侧导入新代理版本。
- `internal/agentupgrade/model.go`：计划、目标、事件和状态常量。
- `internal/agentupgrade/postgres.go`：升级状态持久化和原子状态转换。
- `internal/agentupgrade/service.go`：创建、查询、暂停、继续、取消、重试和回滚。
- `internal/agentupgrade/coordinator.go`：排空、派发、健康验证、批次推进与失败暂停。
- `internal/agentupgrade/loop.go`：可取消的后台扫描循环。
- `internal/agentupgrade/http.go`：升级管理 API。
- `cmd/api/main.go`：装配版本仓库、升级服务、代理事件接收器和编排循环。

### Agent protocol and runtime

- `internal/agentprotocol/messages.go`：扩展心跳的系统、架构、能力和升级运行状态。
- `internal/agentprotocol/upgrade.go`：升级命令、动作、阶段和事件协议。
- `internal/server/connections.go`：向指定服务器发送升级命令。
- `internal/server/http.go`：接收升级事件并把心跳交给升级协调器对账。
- `internal/server/model.go`、`postgres.go`、`query.go`：保存并展示代理能力。
- `internal/agent/client.go`：WebSocket 升级命令队列与事件发送。
- `internal/agent/upgrade_client.go`：幂等处理升级和回滚命令。
- `internal/agentupdate/state.go`：本地升级状态、规格和原子 JSON 文件。
- `internal/agentupdate/stager.go`：下载、摘要校验与固定归档内容验证。
- `internal/agentupdate/applier.go`：root 模式原子安装、确认等待和回滚。
- `internal/agentupdate/systemd_linux.go`：启动升级单元、重启代理与 daemon-reload。
- `internal/agentupdate/systemd_other.go`：非 Linux 明确返回不支持。
- `cmd/agent/main.go`：启动升级客户端并提供 `apply-upgrade` root 子命令。
- `deploy/agent/yunling-agent-upgrade@.service`：root 一次性升级单元。
- `deploy/agent/install.sh`、`package.sh`、`50-yunling-agent.rules`：安装并授权固定升级单元。

### Web console

- `apps/web/src/api/client.ts`：版本、计划、目标、事件和操作 API。
- `apps/web/src/features/servers/ServerSectionTabs.tsx`：服务器模块页签。
- `apps/web/src/features/servers/ServerVersionStatus.tsx`：版本状态展示。
- `apps/web/src/features/servers/ServersPage.tsx`：节点管理页接入版本盘点。
- `apps/web/src/features/servers/AgentUpgradesPage.tsx`：升级概况、活动计划和历史。
- `apps/web/src/features/servers/AgentUpgradeDialog.tsx`：三步创建向导。
- `apps/web/src/features/servers/AgentUpgradePlanPanel.tsx`：目标进度、事件时间线和计划操作。
- `apps/web/src/app/App.tsx`：注册 `/servers/upgrades` 路由。
- `apps/web/src/app/styles.css`：控制台页签、向导、进度和响应式样式。

---

### Task 1: Add the persistence schema

**Files:**
- Create: `migrations/000014_agent_upgrade_management.up.sql`
- Create: `migrations/000014_agent_upgrade_management.down.sql`
- Modify: `internal/store/postgres/migrations_test.go`
- Test: `internal/store/postgres/migrations_test.go`

**Interfaces:**
- Produces tables `agent_releases`, `agent_release_artifacts`, `agent_upgrade_plans`, `agent_upgrade_targets`, `agent_upgrade_events`.
- Produces server columns `agent_os text`, `agent_arch text`, `agent_capabilities jsonb`.
- Produces a partial unique index allowing only one plan in `pending`, `running`, or `paused`.

- [x] **Step 1: Write the failing migration test**

```go
func TestAgentUpgradeMigrationCreatesVersionAndPlanState(t *testing.T) {
	db := startPostgres(t)
	applyMigrations(t, db)
	for _, table := range []string{
		"agent_releases", "agent_release_artifacts", "agent_upgrade_plans",
		"agent_upgrade_targets", "agent_upgrade_events",
	} {
		if !tableExists(t, db, table) {
			t.Fatalf("代理升级迁移后应存在数据表 %q", table)
		}
	}
	for _, index := range []string{"agent_releases_one_recommended_idx", "agent_upgrade_plans_one_active_idx"} {
		if !tableIndexExists(t, db, index) {
			t.Fatalf("代理升级迁移后应存在索引 %q", index)
		}
	}
}
```

- [x] **Step 2: Run the migration test and verify failure**

Run: `go test ./internal/store/postgres -run TestAgentUpgradeMigrationCreatesVersionAndPlanState -count=1`

Expected: FAIL because migration 14 and its tables do not exist.

- [x] **Step 3: Create migration 14 with exact state constraints**

```sql
ALTER TABLE servers
    ADD COLUMN agent_os text NOT NULL DEFAULT '',
    ADD COLUMN agent_arch text NOT NULL DEFAULT '',
    ADD COLUMN agent_capabilities jsonb NOT NULL DEFAULT '[]'::jsonb;

CREATE TABLE agent_releases (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    version text NOT NULL UNIQUE,
    status text NOT NULL DEFAULT 'available' CHECK (status IN ('available', 'withdrawn')),
    recommended boolean NOT NULL DEFAULT false,
    release_notes text NOT NULL DEFAULT '',
    manifest_sha256 char(64) NOT NULL,
    capabilities jsonb NOT NULL DEFAULT '[]'::jsonb,
    created_by uuid REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX agent_releases_one_recommended_idx ON agent_releases ((recommended)) WHERE recommended;

CREATE TABLE agent_release_artifacts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    release_id uuid NOT NULL REFERENCES agent_releases(id) ON DELETE RESTRICT,
    os text NOT NULL CHECK (os = 'linux'),
    arch text NOT NULL CHECK (arch IN ('amd64', 'arm64')),
    file_name text NOT NULL,
    byte_size bigint NOT NULL CHECK (byte_size > 0),
    sha256 char(64) NOT NULL,
    object_key text NOT NULL UNIQUE,
    UNIQUE (release_id, os, arch),
    UNIQUE (release_id, file_name)
);

CREATE TABLE agent_upgrade_plans (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    target_release_id uuid NOT NULL REFERENCES agent_releases(id) ON DELETE RESTRICT,
    status text NOT NULL CHECK (status IN ('pending','running','paused','succeeded','cancelled')),
    first_batch_size integer NOT NULL DEFAULT 1 CHECK (first_batch_size = 1),
    batch_size integer NOT NULL CHECK (batch_size BETWEEN 1 AND 100),
    drain_timeout_seconds integer NOT NULL CHECK (drain_timeout_seconds BETWEEN 60 AND 86400),
    reconnect_timeout_seconds integer NOT NULL CHECK (reconnect_timeout_seconds BETWEEN 30 AND 3600),
    verification_seconds integer NOT NULL CHECK (verification_seconds BETWEEN 10 AND 600),
    current_batch integer NOT NULL DEFAULT 1 CHECK (current_batch > 0),
    created_by uuid NOT NULL REFERENCES users(id),
    pause_reason text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    started_at timestamptz,
    finished_at timestamptz
);

CREATE UNIQUE INDEX agent_upgrade_plans_one_active_idx
    ON agent_upgrade_plans ((true))
    WHERE status IN ('pending','running','paused');

CREATE TABLE agent_upgrade_targets (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    plan_id uuid NOT NULL REFERENCES agent_upgrade_plans(id) ON DELETE CASCADE,
    server_id uuid NOT NULL REFERENCES servers(id) ON DELETE RESTRICT,
    batch_number integer NOT NULL CHECK (batch_number > 0),
    source_version text NOT NULL,
    target_version text NOT NULL,
    source_draining boolean NOT NULL,
    status text NOT NULL CHECK (status IN (
        'waiting','draining','downloading','verifying','installing','reconnecting',
        'health_checking','succeeded','rolling_back','rolled_back',
        'manual_intervention','cancelled'
    )),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    command_id uuid NOT NULL DEFAULT gen_random_uuid(),
    error_code text NOT NULL DEFAULT '',
    error_message text NOT NULL DEFAULT '',
    started_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz,
    UNIQUE (plan_id, server_id)
);

CREATE TABLE agent_upgrade_events (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    plan_id uuid NOT NULL REFERENCES agent_upgrade_plans(id) ON DELETE CASCADE,
    target_id uuid NOT NULL REFERENCES agent_upgrade_targets(id) ON DELETE CASCADE,
    server_id uuid NOT NULL REFERENCES servers(id) ON DELETE RESTRICT,
    command_id uuid NOT NULL,
    stage text NOT NULL,
    error_code text NOT NULL DEFAULT '',
    message text NOT NULL DEFAULT '',
    occurred_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX agent_upgrade_targets_plan_batch_idx ON agent_upgrade_targets (plan_id, batch_number, status);
CREATE INDEX agent_upgrade_events_target_time_idx ON agent_upgrade_events (target_id, occurred_at, id);

INSERT INTO schema_migrations (version) VALUES (14) ON CONFLICT (version) DO NOTHING;
```

- [x] **Step 4: Create the reverse migration**

```sql
DROP TABLE agent_upgrade_events;
DROP TABLE agent_upgrade_targets;
DROP TABLE agent_upgrade_plans;
DROP TABLE agent_release_artifacts;
DROP TABLE agent_releases;
ALTER TABLE servers DROP COLUMN agent_capabilities, DROP COLUMN agent_arch, DROP COLUMN agent_os;
DELETE FROM schema_migrations WHERE version = 14;
```

- [x] **Step 5: Run migration tests**

Run: `go test ./internal/store/postgres -count=1`

Expected: PASS.

- [x] **Step 6: Commit**

```bash
git add migrations/000014_agent_upgrade_management.* internal/store/postgres/migrations_test.go
git commit -m "feat: add agent upgrade persistence schema"
```

---

### Task 2: Report and persist agent upgrade capabilities

**Files:**
- Modify: `internal/agentprotocol/messages.go`
- Modify: `internal/agent/client.go`
- Modify: `internal/agent/client_test.go`
- Modify: `cmd/agent/main.go`
- Modify: `internal/server/model.go`
- Modify: `internal/server/postgres.go`
- Modify: `internal/server/postgres_query_test.go`
- Modify: `internal/server/query.go`
- Modify: `apps/web/src/api/client.ts`

**Interfaces:**
- Produces heartbeat fields `agent_os`, `agent_arch`, `capabilities`.
- Produces `server.ServerView.AgentOS string`, `AgentArch string`, `AgentCapabilities []string` and matching camelCase Web fields.
- Capability name is exactly `self_upgrade_v1`.

- [x] **Step 1: Write failing heartbeat and repository tests**

```go
func TestClientReportsPlatformAndCapabilities(t *testing.T) {
	heartbeat := receiveHeartbeat(t, sender.heartbeats)
	if heartbeat.AgentOS != "linux" || heartbeat.AgentArch != "amd64" {
		t.Fatalf("代理平台上报错误：%s/%s", heartbeat.AgentOS, heartbeat.AgentArch)
	}
	if !slices.Contains(heartbeat.Capabilities, "self_upgrade_v1") {
		t.Fatalf("代理未上报自升级能力：%v", heartbeat.Capabilities)
	}
}
```

Add a PostgreSQL assertion that `SaveHeartbeat` writes the three new fields and `ListServers` returns them.

- [x] **Step 2: Run focused tests and verify failure**

Run: `go test ./internal/agent ./internal/server -run 'TestClientReportsPlatformAndCapabilities|TestPostgresRepositoryPersistsAgentCapabilities' -count=1`

Expected: FAIL because the fields are undefined.

- [x] **Step 3: Extend the protocol and heartbeat client**

```go
type Heartbeat struct {
	ServerID           string    `json:"server_id"`
	Sequence           uint64    `json:"sequence"`
	SentAt             time.Time `json:"sent_at"`
	AgentOS            string    `json:"agent_os,omitempty"`
	AgentArch          string    `json:"agent_arch,omitempty"`
	Capabilities       []string  `json:"capabilities,omitempty"`
	AgentVersion       string    `json:"agent_version"`
}
```

Keep all existing resource fields. Add `WithPlatform(goos, goarch string, capabilities []string)` to `agent.Client` and copy the values into every heartbeat. `cmd/agent/main.go` passes `runtime.GOOS`, `runtime.GOARCH`, and the result of this detection function so non-Linux or partially installed agents never claim upgrade support:

```go
func detectedCapabilities(goos string, stat func(string) (os.FileInfo, error)) []string {
	if goos != "linux" { return nil }
	info, err := stat("/etc/systemd/system/yunling-agent-upgrade@.service")
	if err != nil || !info.Mode().IsRegular() { return nil }
	return []string{"self_upgrade_v1"}
}
```

- [x] **Step 4: Persist and expose the fields**

Encode capabilities as JSON in `SaveHeartbeat`, update `serverViewSelect`, `scanServerView`, `ServerView`, and the Web API mapper. Empty values remain valid for old agents.

- [x] **Step 5: Run focused and regression tests**

Run: `go test ./internal/agent ./internal/server -count=1`

Run: `npm run test:web -- --run apps/web/src/api/client.test.ts`

Expected: PASS.

- [x] **Step 6: Commit**

```bash
git add internal/agentprotocol/messages.go internal/agent internal/server cmd/agent/main.go apps/web/src/api/client.ts apps/web/src/api/client.test.ts
git commit -m "feat: report agent upgrade capabilities"
```

---

### Task 3: Build the immutable multi-version agent repository

**Files:**
- Create: `internal/agentrelease/model.go`
- Create: `internal/agentrelease/postgres.go`
- Create: `internal/agentrelease/postgres_test.go`
- Create: `internal/agentrelease/service.go`
- Create: `internal/agentrelease/service_test.go`
- Create: `internal/agentrelease/management_http.go`
- Create: `internal/agentrelease/management_http_test.go`
- Modify: `internal/agentrelease/http.go`
- Modify: `internal/agentrelease/http_test.go`
- Modify: `internal/artifact/store.go`
- Modify: `internal/artifact/minio.go`

**Interfaces:**
- Produces `agentrelease.Repository` with `Create`, `List`, `Recommended`, `SetRecommended`, `Withdraw`, and `FindArtifact`.
- Produces `agentrelease.Service.Import(ctx, ImportInput) (Release, error)`, `BootstrapFromDirectory(ctx, root)`, and read/query methods.
- Produces `artifact.ErrObjectMissing` for public 404 handling and `artifact.ErrObjectConflict` for immutable-key conflicts.
- Test helpers in `service_test.go`: `validImportInput(version string) ImportInput`, `newMemoryObjectStore() *memoryObjectStore`, and `memoryRepository` implementing the complete repository interface.

- [x] **Step 1: Write failing service tests for immutability**

```go
func TestServiceImportsTwoArchitecturesWithoutOverwrite(t *testing.T) {
	service := NewService(&memoryRepository{}, newMemoryObjectStore(), time.Now)
	release, err := service.Import(context.Background(), validImportInput("0.2.0"))
	if err != nil || release.Version != "0.2.0" || len(release.Artifacts) != 2 {
		t.Fatalf("导入代理版本失败：release=%+v err=%v", release, err)
	}
	_, err = service.Import(context.Background(), validImportInput("0.2.0"))
	if !errors.Is(err, ErrReleaseExists) {
		t.Fatalf("重复版本必须被拒绝：%v", err)
	}
}
```

Use this table for validation coverage, then add one repository transaction test for recommendation switching:

```go
for _, test := range []struct {
	name   string
	mutate func(*ImportInput)
	want   error
}{
	{"缺少 amd64", func(input *ImportInput) { input.Artifacts = input.Artifacts[1:] }, ErrReleaseInvalid},
	{"缺少 arm64", func(input *ImportInput) { input.Artifacts = input.Artifacts[:1] }, ErrReleaseInvalid},
	{"摘要不一致", func(input *ImportInput) { input.Artifacts[0].SHA256 = strings.Repeat("0", 64) }, ErrArtifactMismatch},
	{"对象冲突", func(input *ImportInput) { input.Artifacts[0].Body = strings.NewReader("冲突内容") }, artifact.ErrObjectConflict},
} {
	t.Run(test.name, func(t *testing.T) {
		input := validImportInput("0.2.0")
		test.mutate(&input)
		_, err := service.Import(context.Background(), input)
		if !errors.Is(err, test.want) { t.Fatalf("错误=%v，期望=%v", err, test.want) }
	})
}
```

The repository test sets release A recommended, then release B recommended, and asserts exactly B is recommended. A withdrawn release must return `ErrReleaseWithdrawn` from `SetRecommended`; `Withdraw` on the recommended release must return `ErrRecommendedRelease`. `FindArtifact` must require the exact version, digest, and file name tuple.

`TestBootstrapFromDirectoryOnlyWhenRepositoryEmpty` uses a valid legacy `manifest.json` whose packages do not contain `agent-version` or the upgrade unit, calls bootstrap twice, and asserts the first call imports and recommends one release with empty capabilities while the second performs no object writes. Normal `Import` requires both files in both architectures and records `self_upgrade_v1`.

- [x] **Step 2: Run service tests and verify failure**

Run: `go test ./internal/agentrelease -run 'TestService|TestPostgresRepository' -count=1`

Expected: FAIL because the service and repository are missing.

- [x] **Step 3: Implement model validation and PostgreSQL repository**

```go
type Release struct {
	ID             string     `json:"id"`
	Version        string     `json:"version"`
	Status         string     `json:"status"`
	Recommended    bool       `json:"recommended"`
	ReleaseNotes   string     `json:"release_notes"`
	ManifestSHA256 string     `json:"manifest_sha256"`
	Capabilities   []string   `json:"capabilities"`
	Artifacts      []Artifact `json:"artifacts"`
	CreatedAt      time.Time  `json:"created_at"`
}

type Repository interface {
	Create(context.Context, Release) (Release, error)
	List(context.Context) ([]Release, error)
	Recommended(context.Context) (Release, error)
	SetRecommended(context.Context, string) (Release, error)
	Withdraw(context.Context, string) (Release, error)
	FindArtifact(context.Context, string, string, string) (Release, Artifact, error)
}
```

Use a transaction for release plus artifacts and a transaction that clears the previous recommendation before setting the new one.

- [x] **Step 4: Implement object-backed import and open**

```go
func ObjectKey(version string, artifact Artifact) string {
	return path.Join("agent-releases", version, artifact.SHA256, artifact.FileName)
}

func (s *Service) Open(ctx context.Context, version, digest, fileName string) (io.ReadCloser, Release, Artifact, error) {
	release, artifact, err := s.repository.FindArtifact(ctx, version, digest, fileName)
	if err != nil { return nil, Release{}, Artifact{}, err }
	body, err := s.objects.Open(ctx, artifact.ObjectKey)
	return body, release, artifact, err
}
```

Make `artifact.ErrObjectMissing` exported and preserve `errors.Is` through MinIO errors.

- [x] **Step 5: Replace the public latest/download handler and add management endpoints**

`GET /api/releases/agent/latest` returns `Service.Recommended()`. The immutable download route uses `Service.Open()` and maps unknown or missing objects to 404. `GET /api/agent-releases` lists all versions; `POST /api/agent-releases/{id}/recommend` changes the recommendation; `POST /api/agent-releases/{id}/withdraw` rejects the current recommended version and withdraws any other available version.

- [x] **Step 6: Run package tests**

Run: `go test ./internal/artifact ./internal/agentrelease -count=1`

Expected: PASS.

- [x] **Step 7: Commit**

```bash
git add internal/agentrelease internal/artifact
git commit -m "feat: add immutable multi-version agent repository"
```

---

### Task 4: Add the agent release import command

**Files:**
- Create: `cmd/agent-release/main.go`
- Create: `cmd/agent-release/main_test.go`
- Modify: `deploy/Dockerfile.services`
- Modify: `deploy/docker-compose.yml`
- Modify: `deploy/README.md`

**Interfaces:**
- Consumes `agentrelease.Service.Import(context.Context, agentrelease.ImportInput)` from Task 3.
- Produces CLI `yunling-agent-release import --manifest PATH --directory PATH --notes TEXT [--recommend]`.

- [x] **Step 1: Write failing command parsing and import tests**

```go
func TestParseImportCommand(t *testing.T) {
	configuration, err := parseArgs([]string{
		"import", "--manifest", "/tmp/release/manifest.json",
		"--directory", "/tmp/release", "--notes", "增加自升级器", "--recommend",
	})
	if err != nil || !configuration.Recommend || configuration.ReleaseNotes != "增加自升级器" {
		t.Fatalf("解析导入参数失败：config=%+v err=%v", configuration, err)
	}
}
```

Add a test that two architecture files are opened from the manifest directory and passed to the service with their declared sizes and digests.

- [x] **Step 2: Run command tests and verify failure**

Run: `go test ./cmd/agent-release -count=1`

Expected: FAIL because the command does not exist.

- [x] **Step 3: Implement the command**

Read `YUNLING_DATABASE_URL` and existing `YUNLING_S3_*` variables, parse the manifest with unknown-field rejection, verify each archive contains an `agent-version` file exactly equal to the manifest version, then call `Service.Import`. Print only version, architectures, digest prefixes, and result; do not attempt to execute an arm64 binary on an amd64 import host.

- [x] **Step 4: Package the command in the service image**

Build `/out/yunling-agent-release` in `deploy/Dockerfile.services`, copy it into the API image, and document this exact import form:

```bash
docker compose --env-file deploy/.env -f deploy/docker-compose.yml exec -T api \
  yunling-agent-release import --manifest /release/manifest.json --directory /release \
  --notes "增加代理升级能力" --recommend
```

- [x] **Step 5: Run command and deployment tests**

Run: `go test ./cmd/agent-release ./tests/integration -run 'AgentRelease|Dockerfile' -count=1`

Expected: PASS.

- [x] **Step 6: Commit**

```bash
git add cmd/agent-release deploy
git commit -m "feat: add agent release import command"
```

---

### Task 5: Define upgrade commands, events, and WebSocket transport

**Files:**
- Create: `internal/agentprotocol/upgrade.go`
- Modify: `internal/agent/client.go`
- Modify: `internal/agent/client_test.go`
- Modify: `internal/server/connections.go`
- Modify: `internal/server/http.go`
- Modify: `internal/server/http_test.go`

**Interfaces:**
- Produces `agentprotocol.UpgradeCommand`, `UpgradeEvent`, `UpgradeRuntimeState`, `UpgradeAction`, and `UpgradeStage`.
- Produces `WebSocketSender.ReceiveUpgradeCommand` and `SendUpgradeEvent`.
- Produces `AgentConnectionHub.SendUpgradeCommand`.
- Produces server handler option `WithUpgradeReceiver(UpgradeReceiver)`.

- [x] **Step 1: Write failing transport demultiplexing tests**

```go
func TestWebSocketSenderRoutesUpgradeCommand(t *testing.T) {
	command, err := sender.ReceiveUpgradeCommand(context.Background())
	if err != nil || command.CommandID != "upgrade-1" || command.Action != agentprotocol.UpgradeInstall {
		t.Fatalf("升级命令路由错误：command=%+v err=%v", command, err)
	}
}
```

Add a server handler test that an `agent_upgrade_event` message is routed with the authenticated server ID, and a connection hub test that the command is written to the registered server connection.

- [x] **Step 2: Run tests and verify failure**

Run: `go test ./internal/agent ./internal/server -run Upgrade -count=1`

Expected: FAIL because upgrade protocol types and queues are absent.

- [x] **Step 3: Add exact protocol types**

```go
type UpgradeCommand struct {
	MessageType      string        `json:"message_type"`
	CommandID        string        `json:"command_id"`
	PlanID           string        `json:"plan_id"`
	TargetID         string        `json:"target_id"`
	Action           UpgradeAction `json:"action"`
	SourceVersion    string        `json:"source_version"`
	TargetVersion    string        `json:"target_version"`
	DownloadURL      string        `json:"download_url,omitempty"`
	FileName         string        `json:"file_name,omitempty"`
	ByteSize         int64         `json:"byte_size,omitempty"`
	SHA256           string        `json:"sha256,omitempty"`
	ReconnectTimeout time.Duration `json:"reconnect_timeout"`
}

type UpgradeEvent struct {
	MessageType string       `json:"message_type"`
	CommandID   string       `json:"command_id"`
	TargetID    string       `json:"target_id"`
	Stage       UpgradeStage `json:"stage"`
	OccurredAt  time.Time    `json:"occurred_at"`
	ErrorCode   string       `json:"error_code,omitempty"`
	Message     string       `json:"message,omitempty"`
}
```

Define the actions and stages with these exact wire values:

```go
const (
	UpgradeInstall  UpgradeAction = "install"
	UpgradeRollback UpgradeAction = "rollback"
	StageAccepted       UpgradeStage = "accepted"
	StageDownloading    UpgradeStage = "downloading"
	StageVerifying      UpgradeStage = "verifying"
	StageInstalling     UpgradeStage = "installing"
	StageReconnecting   UpgradeStage = "reconnecting"
	StageSucceeded      UpgradeStage = "succeeded"
	StageRollingBack    UpgradeStage = "rolling_back"
	StageRolledBack     UpgradeStage = "rolled_back"
	StageFailed         UpgradeStage = "failed"
)
```

- [x] **Step 4: Add transport queues and server routing**

Route `message_type == "agent_upgrade_command"` to a dedicated bounded queue and `message_type == "agent_upgrade_event"` to `UpgradeReceiver.ApplyUpgradeEvent`. Upgrade events must not fall through to run events or heartbeats.

- [x] **Step 5: Run transport regression tests**

Run: `go test ./internal/agent ./internal/server -count=1`

Expected: PASS, including existing sync, log, assignment, cancellation, and heartbeat tests.

- [x] **Step 6: Commit**

```bash
git add internal/agentprotocol internal/agent/client* internal/server/connections.go internal/server/http*
git commit -m "feat: add agent upgrade websocket protocol"
```

---

### Task 6: Implement local staging and atomic upgrade application

**Files:**
- Create: `internal/agentupdate/state.go`
- Create: `internal/agentupdate/state_test.go`
- Create: `internal/agentupdate/stager.go`
- Create: `internal/agentupdate/stager_test.go`
- Create: `internal/agentupdate/applier.go`
- Create: `internal/agentupdate/applier_test.go`
- Create: `internal/agentupdate/systemd_linux.go`
- Create: `internal/agentupdate/systemd_other.go`

**Interfaces:**
- Produces `agentupdate.Manager.Stage(context.Context, UpgradeCommand) (Spec, error)`.
- Produces `agentupdate.Manager.StartApply(context.Context, string) error`.
- Produces `agentupdate.Apply(root, commandID string, system SystemController) error` for the root subcommand.
- Local root defaults to `/var/lib/yunling-agent/upgrades`.
- Test helpers: `makeArchive(t, files) []byte`, `validArchive(t) []byte`, `validUpgradeCommand() agentprotocol.UpgradeCommand`, `managerWithArchive([]byte) *Manager`, `managerWithBinaryVersion([]byte, string) *Manager`, `readFile(t, path) string`, and `writeRollbackSpec(t, root, commandID)`.

- [x] **Step 1: Write failing state and archive tests**

```go
func TestStageRejectsUnexpectedArchiveEntry(t *testing.T) {
	archive := makeArchive(t, map[string][]byte{
		"yunling-agent": []byte("binary"),
		"unexpected":    []byte("not allowed"),
	})
	_, err := managerWithArchive(archive).Stage(context.Background(), validUpgradeCommand())
	if !errors.Is(err, ErrInvalidArchive) {
		t.Fatalf("必须拒绝额外归档文件：%v", err)
	}
}
```

Add these concrete subtests beside it:

```go
t.Run("字节数不一致", func(t *testing.T) {
	command := validUpgradeCommand(); command.ByteSize++
	_, err := managerWithArchive(validArchive(t)).Stage(context.Background(), command)
	if !errors.Is(err, ErrArtifactMismatch) { t.Fatalf("错误=%v", err) }
})
t.Run("摘要不一致", func(t *testing.T) {
	command := validUpgradeCommand(); command.SHA256 = strings.Repeat("0", 64)
	_, err := managerWithArchive(validArchive(t)).Stage(context.Background(), command)
	if !errors.Is(err, ErrArtifactMismatch) { t.Fatalf("错误=%v", err) }
})
t.Run("路径穿越", func(t *testing.T) {
	_, err := managerWithArchive(makeArchive(t, map[string][]byte{"../yunling-agent": []byte("x")})).Stage(context.Background(), validUpgradeCommand())
	if !errors.Is(err, ErrInvalidArchive) { t.Fatalf("错误=%v", err) }
})
t.Run("版本不符", func(t *testing.T) {
	manager := managerWithBinaryVersion(validArchive(t), "0.1.9")
	_, err := manager.Stage(context.Background(), validUpgradeCommand())
	if !errors.Is(err, ErrVersionMismatch) { t.Fatalf("错误=%v", err) }
})
```

`state_test.go` writes the same command twice and asserts the second call returns the persisted `Spec` without another download; it also asserts `spec.json` is valid after atomic replacement and no `spec.json.tmp` remains.

- [x] **Step 2: Run staging tests and verify failure**

Run: `go test ./internal/agentupdate -run 'Stage|State' -count=1`

Expected: FAIL because the package is missing.

- [x] **Step 3: Implement staging**

Accept exactly these package files: `agent-version`, `yunling-agent`, `install.sh`, `yunling-agent.service`, `yunling-run@.service`, `yunling-agent-upgrade@.service`, and `50-yunling-agent.rules`. Require `agent-version` to equal the command target version. Download into `<root>/<commandID>/download.tmp`, validate before extracting, write extracted files under `stage`, and atomically rename `spec.json.tmp` to `spec.json`.

- [x] **Step 4: Write failing apply and rollback tests**

```go
func TestApplyRestoresPreviousVersionWhenReconnectTimesOut(t *testing.T) {
	system := &fakeSystemController{waitForConfirmationError: context.DeadlineExceeded}
	err := Apply(root, "upgrade-1", system)
	if err == nil || readFile(t, currentBinary) != "old-binary" {
		t.Fatalf("重连超时必须恢复旧版本：err=%v", err)
	}
	if system.restartCalls != 2 {
		t.Fatalf("安装和回滚应各重启一次代理：%d", system.restartCalls)
	}
}
```

Add these apply assertions:

```go
func TestApplySuccessKeepsIdentityAndCache(t *testing.T) {
	system := &fakeSystemController{confirm: true}
	if err := Apply(root, "upgrade-1", system); err != nil { t.Fatal(err) }
	if readFile(t, currentBinary) != "new-binary" { t.Fatal("未安装新代理") }
	if readFile(t, credentialsPath) != "credentials" || readFile(t, cachedScriptPath) != "script" {
		t.Fatal("升级不得修改身份或脚本缓存")
	}
}

func TestExplicitRollbackRestoresBackup(t *testing.T) {
	writeRollbackSpec(t, root, "rollback-1")
	if err := Apply(root, "rollback-1", &fakeSystemController{confirm: true}); err != nil { t.Fatal(err) }
	if readFile(t, currentBinary) != "old-binary" { t.Fatal("未恢复备份代理") }
}
```

The crash-recovery test persists phase `files_replaced`, invokes `Apply` again, and asserts it resumes at daemon-reload without taking a second backup.

- [x] **Step 5: Implement apply and rollback**

The root applier revalidates the staged digest, copies the current managed files to `backup`, installs new files to same-directory temporary names, atomically renames them, calls daemon-reload, restarts the agent, and waits for `<root>/<commandID>/connected`. On timeout it restores `backup`, reloads systemd, and restarts the old agent.

- [x] **Step 6: Run local updater tests**

Run: `go test ./internal/agentupdate -count=1`

Expected: PASS on Windows using fake system controllers; Linux-specific command construction is covered without mutating the host.

- [x] **Step 7: Commit**

```bash
git add internal/agentupdate
git commit -m "feat: add atomic local agent updater"
```

---

### Task 7: Install the restricted systemd upgrade unit

**Files:**
- Create: `deploy/agent/yunling-agent-upgrade@.service`
- Modify: `deploy/agent/50-yunling-agent.rules`
- Modify: `deploy/agent/install.sh`
- Modify: `deploy/agent/package.sh`
- Modify: `deploy/agent/install_test.sh`
- Modify: `deploy/agent/package_test.sh`
- Modify: `apps/web/src/deploy-polkit.test.js`
- Modify: `.github/workflows/ci.yml`
- Modify: `cmd/agent/main.go`
- Modify: `cmd/agent/main_test.go`

**Interfaces:**
- Consumes `agentupdate.Apply` from Task 6.
- Produces root command `yunling-agent apply-upgrade COMMAND_ID`.
- Produces fixed unit name `yunling-agent-upgrade@<command-id>.service`.

- [ ] **Step 1: Add failing package, installer, and policy assertions**

Update expected archive contents to include `agent-version` and `yunling-agent-upgrade@.service`. Assert `agent-version` equals the package command version, the installer writes the unit root-owned with mode `0644`, and polkit allows only `start` for `/^yunling-agent-upgrade@[A-Za-z0-9_-]+\.service$/` while rejecting arbitrary services and `stop` for the upgrade unit.

- [ ] **Step 2: Run deployment tests and verify failure**

Run: `bash deploy/agent/package_test.sh`

Run: `bash deploy/agent/install_test.sh`

Run: `node --test apps/web/src/deploy-polkit.test.js`

Expected: FAIL because the upgrade unit is not packaged or authorized.

- [ ] **Step 3: Add the root one-shot unit**

```ini
[Unit]
Description=云令代理升级 %i
After=network-online.target

[Service]
Type=oneshot
User=root
ExecStart=/usr/local/bin/yunling-agent apply-upgrade %i
PrivateTmp=true
ProtectHome=true
ProtectSystem=strict
ReadWritePaths=/usr/local/bin /etc/systemd/system /etc/polkit-1/rules.d /var/lib/yunling-agent/upgrades
```

- [ ] **Step 4: Extend installer, packager, and polkit rule**

Install the template unit with the existing systemd assets. Package it in both architectures. Restrict command IDs to 1–128 ASCII letters, digits, underscore, or hyphen before using them in paths or unit names.

- [ ] **Step 5: Add the `apply-upgrade` command before normal agent startup**

```go
if len(os.Args) == 3 && os.Args[1] == "apply-upgrade" {
	if err := agentupdate.Apply(defaultUpgradeRoot, os.Args[2], agentupdate.NewSystemController()); err != nil {
		log.Fatalf("应用代理升级失败：%v", err)
	}
	return
}
```

- [ ] **Step 6: Run deployment and command tests**

Extend the CI real-build step with these assertions:

```bash
test "$("$RUNNER_TEMP/yunling-agent-linux-amd64" version)" = "$AGENT_VERSION"
file "$RUNNER_TEMP/yunling-agent-linux-amd64" | grep -F 'x86-64'
file "$RUNNER_TEMP/yunling-agent-linux-arm64" | grep -F 'ARM aarch64'
for arch in amd64 arm64; do
  tar -xOf "$release_dir/yunling-agent-${AGENT_VERSION}-linux-${arch}.tar.gz" agent-version | grep -Fx "$AGENT_VERSION"
done
```

Run: `bash deploy/agent/package_test.sh`

Run: `bash deploy/agent/install_test.sh`

Run: `node --test apps/web/src/deploy-polkit.test.js`

Run: `go test ./cmd/agent ./internal/agentupdate -count=1`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add deploy/agent apps/web/src/deploy-polkit.test.js .github/workflows/ci.yml cmd/agent
git commit -m "feat: install restricted agent upgrade service"
```

---

### Task 8: Run the upgrade client inside the agent

**Files:**
- Create: `internal/agent/upgrade_client.go`
- Create: `internal/agent/upgrade_client_test.go`
- Modify: `internal/agent/client.go`
- Modify: `internal/agent/collector.go`
- Modify: `cmd/agent/main.go`
- Modify: `cmd/agent/main_test.go`

**Interfaces:**
- Consumes `agentupdate.Manager` from Task 6 and upgrade transport from Task 5.
- Produces `agent.NewUpgradeClient(manager, transport, now)`.
- Produces `agentupdate.ConfirmReconnect(root, commandID, version)` called after WebSocket connection.
- Produces heartbeat `Upgrade *agentprotocol.UpgradeRuntimeState`.
- Test helpers: `validUpgradeCommand(id string) agentprotocol.UpgradeCommand`, `runUntilEvents(t, client, count)`, and `writePendingState(t, root, commandID, targetVersion)`.

- [ ] **Step 1: Write failing upgrade client tests**

```go
func TestUpgradeClientStagesReportsAndStartsInstall(t *testing.T) {
	client := NewUpgradeClient(manager, transport, fixedNow)
	if err := client.Run(ctx); err != nil { t.Fatal(err) }
	if manager.stageCalls != 1 || manager.startCalls != 1 {
		t.Fatalf("升级处理次数错误：stage=%d start=%d", manager.stageCalls, manager.startCalls)
	}
	assertStages(t, transport.events, "accepted", "downloading", "verifying", "installing")
}
```

Include these additional tests:

```go
func TestUpgradeClientDoesNotRestageDuplicateCommand(t *testing.T) {
	transport.commands <- validUpgradeCommand("upgrade-1")
	transport.commands <- validUpgradeCommand("upgrade-1")
	runUntilEvents(t, client, 2)
	if manager.stageCalls != 1 { t.Fatalf("重复命令发生重复暂存：%d", manager.stageCalls) }
}

func TestConfirmReconnectRequiresTargetVersion(t *testing.T) {
	writePendingState(t, root, "upgrade-1", "0.2.0")
	if err := agentupdate.ConfirmReconnect(root, "upgrade-1", "0.1.0"); !errors.Is(err, agentupdate.ErrVersionMismatch) {
		t.Fatalf("错误版本不得确认重连：%v", err)
	}
}
```

The rollback test sends `Action: agentprotocol.UpgradeRollback` and asserts `manager.rollbackCalls == 1`. The failure test makes `Stage` return `errors.New("下载中断")` and asserts the final event has stage `failed`, error code `stage_failed`, and message `代理升级暂存失败：下载中断`.

- [ ] **Step 2: Run focused tests and verify failure**

Run: `go test ./internal/agent -run UpgradeClient -count=1`

Expected: FAIL because the upgrade client is missing.

- [ ] **Step 3: Implement the upgrade client**

Receive one upgrade command at a time, persist `accepted`, emit each stage transition, delegate staging and systemd start to `agentupdate.Manager`, and rely on persisted local state for duplicate commands. The install path ends after starting the root unit because the service is about to restart.

- [ ] **Step 4: Confirm reconnect and expose runtime state**

After `DialHeartbeatSender` succeeds, read pending local state. If the running version equals its target version, atomically create the connected marker and send a `reconnecting` event. The collector reads the local state snapshot and includes it in heartbeats until the control plane marks the command complete.

- [ ] **Step 5: Start the fifth agent loop**

Increase the error channel capacity to 5 and run `upgradeClient.Run(ctx)` beside heartbeat, sync, execution, and log clients.

- [ ] **Step 6: Run agent tests**

Run: `go test ./internal/agent ./cmd/agent -count=1`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/agent cmd/agent/main.go cmd/agent/main_test.go
git commit -m "feat: run agent self-upgrade client"
```

---

### Task 9: Implement upgrade plan repository and management service

**Files:**
- Create: `internal/agentupgrade/model.go`
- Create: `internal/agentupgrade/postgres.go`
- Create: `internal/agentupgrade/postgres_test.go`
- Create: `internal/agentupgrade/service.go`
- Create: `internal/agentupgrade/service_test.go`

**Interfaces:**
- Produces `agentupgrade.Service.CreatePlan`, `ListPlans`, `Plan`, `Pause`, `Resume`, `Cancel`, `RetryTarget`, and `CreateRollbackPlan`.
- Produces transaction-safe target transitions used by Task 10.
- Consumes agent release metadata and server capability records.
- Test helpers: `validPlanInput() CreatePlanInput`, `inputWithServers(...string)`, `inputWithDrainTimeout(int)`, `inputWithServer(string)`, `inputWithRelease(string)`, and `serviceForCase(string) *Service`.

- [ ] **Step 1: Write failing plan creation tests**

```go
func TestCreatePlanMakesSingleNodeCanaryAndConfiguredBatches(t *testing.T) {
	plan, err := service.CreatePlan(ctx, CreatePlanInput{
		TargetReleaseID: "release-2", ServerIDs: []string{"s1", "s2", "s3", "s4"}, BatchSize: 2,
	})
	if err != nil { t.Fatal(err) }
	if got := targetBatches(plan.Targets); !reflect.DeepEqual(got, []int{1, 2, 2, 3}) {
		t.Fatalf("批次划分错误：%v", got)
	}
}
```

Use a table-driven validation test with these exact expected errors:

```go
for _, test := range []struct { name string; input CreatePlanInput; want error }{
	{"已有活动计划", validPlanInput(), ErrActivePlanExists},
	{"重复服务器", inputWithServers("s1", "s1"), ErrInvalidPlan},
	{"超时无效", inputWithDrainTimeout(59), ErrInvalidPlan},
	{"服务器离线", inputWithServer("offline"), ErrServerIneligible},
	{"服务器已停用", inputWithServer("disabled"), ErrServerIneligible},
	{"缺少升级能力", inputWithServer("legacy"), ErrUpgradeUnsupported},
	{"缺少匹配架构", inputWithServer("unsupported-arch"), ErrArtifactUnavailable},
	{"已经是目标版本", inputWithServer("already-current"), ErrNoUpgradeNeeded},
	{"目标版本没有升级器", inputWithRelease("legacy-release"), ErrUpgradeUnsupported},
} {
	t.Run(test.name, func(t *testing.T) {
		_, err := serviceForCase(test.name).CreatePlan(ctx, test.input)
		if !errors.Is(err, test.want) { t.Fatalf("错误=%v，期望=%v", err, test.want) }
	})
}
```

The successful creation test must also assert `SourceDraining` equals each server's pre-plan drain flag.

- [ ] **Step 2: Run service tests and verify failure**

Run: `go test ./internal/agentupgrade -run 'CreatePlan|Pause|Resume|Cancel|Retry|Rollback' -count=1`

Expected: FAIL because the package does not exist.

- [ ] **Step 3: Implement models and plan creation**

```go
type CreatePlanInput struct {
	TargetReleaseID       string   `json:"target_release_id"`
	ServerIDs             []string `json:"server_ids"`
	BatchSize             int      `json:"batch_size"`
	DrainTimeoutSeconds   int      `json:"drain_timeout_seconds"`
	ReconnectTimeoutSeconds int    `json:"reconnect_timeout_seconds"`
	VerificationSeconds  int      `json:"verification_seconds"`
}
```

Defaults are 1 node, 3600 seconds drain, 120 seconds reconnect, and 30 seconds verification. The repository creates the plan and all targets in one transaction and relies on the partial unique index for the one-active-plan invariant.

- [ ] **Step 4: Implement management transitions**

Pause changes `pending` or `running` to `paused`. Resume changes `paused` to `running` after revalidation. Cancel marks only `waiting` targets cancelled. Retry creates a new command ID and returns a rolled-back target to `draining`. `CreateRollbackPlan(planID, targetID, actorID)` requires the referenced target to be successful and no active plan to exist, then creates a new one-node plan whose target release is the stored source version.

- [ ] **Step 5: Run service and repository tests**

Run: `go test ./internal/agentupgrade -count=1`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/agentupgrade
git commit -m "feat: add agent upgrade plan service"
```

---

### Task 10: Add orchestration, event reconciliation, and automatic rollback

**Files:**
- Create: `internal/agentupgrade/coordinator.go`
- Create: `internal/agentupgrade/coordinator_test.go`
- Create: `internal/agentupgrade/loop.go`
- Create: `internal/agentupgrade/loop_test.go`
- Modify: `internal/server/http.go`
- Modify: `internal/server/http_test.go`
- Modify: `internal/server/connections.go`

**Interfaces:**
- Produces `Coordinator.Scan(context.Context) error`.
- Produces `Coordinator.ApplyUpgradeEvent(context.Context, string, agentprotocol.UpgradeEvent) error`.
- Produces `Coordinator.ObserveHeartbeat(context.Context, agentprotocol.Heartbeat) error`.
- Consumes `AgentConnectionHub.SendUpgradeCommand` and version repository artifact lookup.
- Test helpers: `failedEvent(targetID string) agentprotocol.UpgradeEvent` and `assertTargetStatus(t, store, targetID, want)`.

- [ ] **Step 1: Write failing orchestration tests**

```go
func TestCoordinatorWaitsForTasksThenDispatchesCanary(t *testing.T) {
	store.runningTasks = 1
	if err := coordinator.Scan(ctx); err != nil { t.Fatal(err) }
	if sender.calls != 0 || !store.draining { t.Fatal("运行任务未结束时只能排空") }
	store.runningTasks = 0
	if err := coordinator.Scan(ctx); err != nil { t.Fatal(err) }
	if sender.calls != 1 || sender.last.TargetID != "target-canary" {
		t.Fatalf("未派发首批升级：%+v", sender.last)
	}
}
```

Include these state-transition assertions:

```go
func TestCoordinatorPausesAndRollsBackOnlyFailedBatch(t *testing.T) {
	store.targets = []Target{
		{ID: "canary", BatchNumber: 1, Status: TargetSucceeded},
		{ID: "batch-2-a", BatchNumber: 2, Status: TargetInstalling},
		{ID: "batch-2-b", BatchNumber: 2, Status: TargetReconnecting},
		{ID: "waiting", BatchNumber: 3, Status: TargetWaiting},
	}
	err := coordinator.ApplyUpgradeEvent(ctx, "server-a", failedEvent("batch-2-a"))
	if err != nil { t.Fatal(err) }
	if store.plan.Status != PlanPaused { t.Fatalf("计划未暂停：%s", store.plan.Status) }
	assertTargetStatus(t, store, "canary", TargetSucceeded)
	assertTargetStatus(t, store, "batch-2-a", TargetRollingBack)
	assertTargetStatus(t, store, "batch-2-b", TargetRollingBack)
	assertTargetStatus(t, store, "waiting", TargetWaiting)
}

func TestCoordinatorMarksManualInterventionWhenRollbackFails(t *testing.T) {
	err := coordinator.ApplyUpgradeEvent(ctx, "server-a", agentprotocol.UpgradeEvent{TargetID: "target-a", Stage: agentprotocol.StageFailed, ErrorCode: "rollback_failed"})
	if err != nil { t.Fatal(err) }
	assertTargetStatus(t, store, "target-a", TargetManualIntervention)
	if !store.draining { t.Fatal("回滚失败节点必须保持排空") }
}
```

Separate tests set the drain deadline in the past and expect `PlanPaused`, keep an offline target in `draining` before its deadline, advance a fake clock across `verification_seconds` and expect success plus the next batch, and reconstruct a coordinator against persisted `reconnecting` state to prove restart reconciliation.

- [ ] **Step 2: Run coordinator tests and verify failure**

Run: `go test ./internal/agentupgrade -run 'Coordinator|RunLoop' -count=1`

Expected: FAIL because coordinator and loop are missing.

- [ ] **Step 3: Implement deterministic scan transitions**

For the current plan and batch: request drain, inspect latest running task count and online state, dispatch install commands only when idle, update deadlines, and never start a later batch until every current target has passed its verification window. Every transition uses a compare-and-set update on the expected current status.

- [ ] **Step 4: Implement event and heartbeat reconciliation**

Map agent stages to target stages only when `serverID`, `targetID`, and `commandID` match. A heartbeat with matching target version advances `reconnecting` to `health_checking`; continuous valid heartbeats through `verification_seconds` mark success. A mismatched version after install dispatches rollback.

- [ ] **Step 5: Implement failure pause and failed-batch rollback**

On the first failure, atomically pause the plan and mark every started non-success target in the current batch for rollback. Do not modify targets in earlier successful batches. Preserve the pre-upgrade drain state when restoring scheduling after success.

- [ ] **Step 6: Add the cancellable loop**

```go
func RunLoop(ctx context.Context, coordinator interface{ Scan(context.Context) error }, interval time.Duration, onError func(error)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := coordinator.Scan(ctx); err != nil { onError(err) }
		select {
		case <-ctx.Done(): return
		case <-ticker.C:
		}
	}
}
```

- [ ] **Step 7: Run upgrade and server tests**

Run: `go test ./internal/agentupgrade ./internal/server -count=1`

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/agentupgrade internal/server
git commit -m "feat: orchestrate staged agent upgrades"
```

---

### Task 11: Expose management APIs and assemble the control plane

**Files:**
- Create: `internal/agentupgrade/http.go`
- Create: `internal/agentupgrade/http_test.go`
- Modify: `cmd/api/main.go`
- Modify: `cmd/api/main_test.go`
- Modify: `internal/audit/model.go`
- Modify: `internal/audit/middleware.go`
- Modify: `internal/audit/middleware_test.go`

**Interfaces:**
- Consumes release service from Task 3 and upgrade service/coordinator from Tasks 9–10.
- Produces the REST routes defined in design section 11.
- Runs coordinator scan every 2 seconds with a process-lifetime context.

- [ ] **Step 1: Write failing HTTP behavior tests**

```go
func TestCreateUpgradePlanReturnsChinesePlan(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/api/agent-upgrades", strings.NewReader(`{
		"target_release_id":"release-2","server_ids":["server-1"],"batch_size":1,
		"drain_timeout_seconds":3600,"reconnect_timeout_seconds":120,"verification_seconds":30
	}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || !strings.Contains(response.Body.String(), `"status":"running"`) {
		t.Fatalf("创建升级计划响应错误：%d %s", response.Code, response.Body.String())
	}
}
```

Register a table of HTTP cases with these exact outcomes:

```go
for _, test := range []struct { method, path, body string; status int }{
	{http.MethodGet, "/api/agent-upgrades", "", http.StatusOK},
	{http.MethodGet, "/api/agent-upgrades/missing", "", http.StatusNotFound},
	{http.MethodPost, "/api/agent-upgrades/plan-1/pause", "{}", http.StatusOK},
	{http.MethodPost, "/api/agent-upgrades/plan-1/resume", "{}", http.StatusOK},
	{http.MethodPost, "/api/agent-upgrades/plan-1/cancel", "{}", http.StatusOK},
	{http.MethodPost, "/api/agent-upgrades/plan-1/targets/target-1/retry", "{}", http.StatusOK},
	{http.MethodPost, "/api/agent-upgrades/plan-1/targets/target-1/rollback", "{}", http.StatusCreated},
	{http.MethodPost, "/api/agent-upgrades", "{", http.StatusBadRequest},
} {
	t.Run(test.method+test.path, func(t *testing.T) { assertHTTPStatus(t, handler, test.method, test.path, test.body, test.status) })
}
```

Run the same mutation requests as a non-admin and assert 403; run both GET requests as a member and assert 200. Assert audit actions `agent_release.recommend`, `agent_release.withdraw`, `agent_upgrade.create`, `pause`, `resume`, `cancel`, `retry`, and `rollback` are persisted.

- [ ] **Step 2: Run HTTP tests and verify failure**

Run: `go test ./internal/agentupgrade ./cmd/api ./internal/audit -run Upgrade -count=1`

Expected: FAIL because routes and assembly are absent.

- [ ] **Step 3: Implement handlers and error mapping**

Map invalid input to 400, absent records to 404, an existing active plan or illegal transition to 409, withdrawal of the recommended version to 409, non-admin mutation to 403, and unexpected failures to a bounded Chinese 500 response. Return empty arrays as `[]`, not `null`.

- [ ] **Step 4: Assemble services in `cmd/api/main.go`**

Create the shared MinIO store once, initialize release and upgrade repositories, call `BootstrapFromDirectory(ctx, YUNLING_AGENT_RELEASE_DIR)` before registering the new public release handler, register protected management routes, pass the coordinator into `server.Handler`, and start `agentupgrade.RunLoop` with the same connection hub used for task dispatch. If bootstrap fails, install the release-unavailable handler and log a bounded Chinese error while leaving other APIs active.

- [ ] **Step 5: Run API and audit tests**

Run: `go test ./internal/agentupgrade ./internal/agentrelease ./internal/audit ./cmd/api -count=1`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/agentupgrade internal/audit cmd/api/main.go cmd/api/main_test.go
git commit -m "feat: expose agent upgrade management api"
```

---

### Task 12: Add server version inventory to the Chinese console

**Files:**
- Create: `apps/web/src/features/servers/ServerSectionTabs.tsx`
- Create: `apps/web/src/features/servers/ServerVersionStatus.tsx`
- Create: `apps/web/src/features/servers/ServerVersionStatus.test.tsx`
- Modify: `apps/web/src/features/servers/ServersPage.tsx`
- Modify: `apps/web/src/features/servers/ServersPage.test.tsx`
- Modify: `apps/web/src/features/servers/ServerDrawer.tsx`
- Modify: `apps/web/src/api/client.ts`
- Modify: `apps/web/src/api/client.test.ts`
- Modify: `apps/web/src/app/styles.css`

**Interfaces:**
- Consumes `GET /api/agent-releases` and enriched server records.
- Produces `ServerVersionStatus` labels: `已是最新版`, `可升级`, `升级中`, `版本未知`, `升级失败`, `需人工升级基线`.

- [ ] **Step 1: Write failing version status tests**

```tsx
it('旧代理显示需人工升级基线', () => {
  render(<ServerVersionStatus server={server({ agentVersion: '0.1.0', agentCapabilities: [] })} recommendedVersion="0.2.0" />)
  expect(screen.getByText('需人工升级基线')).toBeVisible()
})
```

Add a table-driven component test:

```tsx
it.each([
  ['0.2.0', ['self_upgrade_v1'], undefined, '已是最新版'],
  ['0.1.0', ['self_upgrade_v1'], undefined, '可升级'],
  ['0.1.0', ['self_upgrade_v1'], 'installing', '升级中'],
  ['', ['self_upgrade_v1'], undefined, '版本未知'],
  ['0.1.0', ['self_upgrade_v1'], 'manual_intervention', '升级失败'],
])('计算服务器版本状态', (version, capabilities, upgradeStatus, expected) => {
  render(<ServerVersionStatus server={server({ agentVersion: version, agentCapabilities: capabilities, upgradeStatus })} recommendedVersion="0.2.0" />)
  expect(screen.getByText(expected)).toBeVisible()
})
```

`ServersPage.test.tsx` must assert both tabs, the new version column, and drawer values `Linux / amd64` and `支持控制台升级`.

- [ ] **Step 2: Run Web tests and verify failure**

Run: `npm run test:web -- --run apps/web/src/features/servers/ServerVersionStatus.test.tsx apps/web/src/features/servers/ServersPage.test.tsx`

Expected: FAIL because components and fields are absent.

- [ ] **Step 3: Add API types and mappers**

```ts
export interface AgentRelease {
  id: string
  version: string
  status: 'available' | 'withdrawn'
  recommended: boolean
  releaseNotes: string
  capabilities: string[]
  createdAt: string
  artifacts: AgentReleaseArtifact[]
}
```

Add `agentOS`, `agentArch`, `agentCapabilities`, and optional `upgradeStatus` to `ServerView`. Add `getAgentReleases()`.

- [ ] **Step 4: Implement tabs and status display**

Render `节点管理` linking to `/servers` and `代理升级` linking to `/servers/upgrades`. Add the agent version column without removing CPU, memory, running tasks, labels, drain, or enable actions.

- [ ] **Step 5: Run server page and API tests**

Run: `npm run test:web -- --run apps/web/src/features/servers apps/web/src/api/client.test.ts`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add apps/web/src/api apps/web/src/features/servers apps/web/src/app/styles.css
git commit -m "feat: show agent version inventory"
```

---

### Task 13: Build the agent upgrade console, verify end to end, and document operations

**Files:**
- Create: `apps/web/src/features/servers/AgentUpgradesPage.tsx`
- Create: `apps/web/src/features/servers/AgentUpgradesPage.test.tsx`
- Create: `apps/web/src/features/servers/AgentUpgradeDialog.tsx`
- Create: `apps/web/src/features/servers/AgentUpgradeDialog.test.tsx`
- Create: `apps/web/src/features/servers/AgentUpgradePlanPanel.tsx`
- Create: `apps/web/src/features/servers/AgentUpgradePlanPanel.test.tsx`
- Modify: `apps/web/src/api/client.ts`
- Modify: `apps/web/src/api/client.test.ts`
- Modify: `apps/web/src/app/App.tsx`
- Modify: `apps/web/src/app/App.test.tsx`
- Modify: `apps/web/src/app/styles.css`
- Create: `tests/integration/agent_upgrade_test.go`
- Modify: `deploy/README.md`
- Modify: `deploy/PRODUCTION.md`
- Modify: `README.md`

**Interfaces:**
- Consumes all management APIs from Task 11.
- Produces route `/servers/upgrades` and the complete Chinese workflow.
- Web test helpers: `renderUpgradePage`, `renderDialog`, `recommendedRelease`, `availableRelease`, and `planWithTargetStatus` use only deterministic mocked API data.
- Go integration helper `newUpgradeIntegrationFixture(t)` owns a real temporary PostgreSQL database and fake per-server WebSocket senders; its methods have the exact names used in Step 7.

- [ ] **Step 1: Add failing API and route tests**

Define these client-facing shapes and operations, then test each method, URL, JSON body, and Chinese error propagation:

```ts
export interface AgentUpgradePlan {
  id: string
  targetVersion: string
  status: 'pending' | 'running' | 'paused' | 'succeeded' | 'cancelled'
  currentBatch: number
  targets: AgentUpgradeTarget[]
  events: AgentUpgradeEvent[]
  createdAt: string
}

export interface AgentUpgradeTarget {
  id: string
  serverId: string
  serverName: string
  batchNumber: number
  sourceVersion: string
  targetVersion: string
  status: string
  attempts: number
  errorMessage: string
  updatedAt: string
}

export interface AgentUpgradeEvent {
  id: string
  targetId: string
  stage: string
  message: string
  occurredAt: string
}

export interface CreateAgentUpgradePlanInput {
  targetReleaseId: string
  serverIds: string[]
  batchSize: number
  drainTimeoutSeconds: number
  reconnectTimeoutSeconds: number
  verificationSeconds: number
}
```

The client exports `getAgentUpgradePlans`, `getAgentUpgradePlan`, `createAgentUpgradePlan`, `pauseAgentUpgradePlan`, `resumeAgentUpgradePlan`, `cancelAgentUpgradePlan`, `retryAgentUpgradeTarget`, `rollbackAgentUpgradeTarget`, `recommendAgentRelease`, and `withdrawAgentRelease`. `App.test.tsx` navigates to `/servers/upgrades` and asserts the `代理升级` heading.

- [ ] **Step 2: Add failing page summary and empty-state tests**

```tsx
it('显示代理升级概况和空状态', async () => {
  renderUpgradePage()
  expect(await screen.findByText('代理升级')).toBeVisible()
  expect(screen.getByText('推荐版本')).toBeVisible()
  expect(screen.getByText('当前没有进行中的升级计划')).toBeVisible()
  expect(screen.getByRole('button', { name: '创建升级计划' })).toBeEnabled()
})
```

```tsx
it('允许管理非推荐版本但保护推荐版本', async () => {
  const user = userEvent.setup()
  renderUpgradePage({ releases: [recommendedRelease('0.2.0'), availableRelease('0.1.0')] })
  const oldRow = screen.getByRole('row', { name: /0.1.0/ })
  await user.click(within(oldRow).getByRole('button', { name: '撤回版本' }))
  expect(withdrawAgentRelease).toHaveBeenCalledTimes(1)
  const currentRow = screen.getByRole('row', { name: /0.2.0/ })
  expect(within(currentRow).queryByRole('button', { name: '撤回版本' })).not.toBeInTheDocument()
})
```

- [ ] **Step 3: Add failing three-step wizard tests**

```tsx
it('创建首批单节点的分批升级计划', async () => {
  const user = userEvent.setup()
  renderDialog()
  await user.click(await screen.findByRole('radio', { name: /0.2.0/ }))
  await user.click(screen.getByRole('button', { name: '下一步' }))
  expect(screen.getByLabelText(/旧代理节点/)).toBeDisabled()
  await user.click(screen.getByLabelText(/京东云执行节点/))
  await user.click(screen.getByRole('button', { name: '下一步' }))
  expect(screen.getByText('首批固定 1 台')).toBeVisible()
  await user.clear(screen.getByLabelText('后续每批服务器数'))
  await user.type(screen.getByLabelText('后续每批服务器数'), '2')
  await user.click(screen.getByRole('button', { name: '启动升级计划' }))
  expect(createAgentUpgradePlan).toHaveBeenCalledTimes(1)
})
```

Additional assertions filter by `京东云`, `华东 1`, `用途=批处理`, and `0.1.0`; validate each timeout boundary; keep the submit button disabled while the request is pending; and restore focus to `创建升级计划` after close.

- [ ] **Step 4: Implement the page and wizard**

The page loads releases, servers, and plans together. The wizard submits only server IDs that remain eligible at confirmation time. Use native form labels and buttons, keep every validation error adjacent to its field, and preserve existing console colors and spacing.

- [ ] **Step 5: Add failing plan panel action tests**

```tsx
it.each([
  ['draining', '等待任务结束'], ['downloading', '下载安装包'], ['verifying', '校验安装包'],
  ['installing', '安装新版本'], ['reconnecting', '等待代理重连'],
  ['health_checking', '健康验证'], ['rolling_back', '正在回滚'],
  ['manual_intervention', '需要人工处理'],
])('显示中文目标阶段 %s', (status, label) => {
  render(<AgentUpgradePlanPanel plan={planWithTargetStatus(status)} />)
  expect(screen.getByText(label)).toBeVisible()
})
```

For each action button, click once while its mocked promise is unresolved and assert the button is disabled, resolve it, then assert `getAgentUpgradePlan(plan.id)` refreshes the detail. A successful target exposes `回滚此节点`; a rolled-back target exposes `重试此节点`.

- [ ] **Step 6: Implement plan detail and history**

Poll the active plan every 5 seconds while its status is `running` or `paused`; stop polling terminal plans. Render target progress and event timestamps without exposing raw stack traces. Keep history read-only and newest first.

- [ ] **Step 7: Add the backend integration scenario**

```go
func TestAgentUpgradeCanaryThenFailedBatchRollback(t *testing.T) {
	fixture := newUpgradeIntegrationFixture(t)
	plan := fixture.CreatePlan([]string{"server-1", "server-2", "server-3"}, 2)
	fixture.SetRunningTasks("server-1", 1)
	fixture.Scan()
	fixture.AssertNoCommand("server-1")
	fixture.SetRunningTasks("server-1", 0)
	fixture.Scan()
	fixture.AssertInstallCommand("server-1", plan.ID)
	fixture.SucceedAndVerify("server-1")
	fixture.Scan()
	fixture.AssertInstallCommand("server-2", plan.ID)
	fixture.AssertInstallCommand("server-3", plan.ID)
	fixture.Fail("server-2", "install_failed")
	fixture.AssertPlanStatus(plan.ID, "paused")
	fixture.AssertNoRollback("server-1")
	fixture.AssertRollback("server-2")
	fixture.AssertRollback("server-3")
}
```

- [ ] **Step 8: Document baseline upgrade and normal operation**

Document the one-time manual baseline update for legacy nodes, importing a new release, creating a plan, reading stages, retrying, rollback behavior, and recovering `manual_intervention`. Documentation commands use the literal sample path `/srv/yunling-agent-releases/0.2.0` and contain no credentials.

- [ ] **Step 9: Run the complete verification suite**

Run: `go test -p 1 ./... -count=1`

Run: `npm run test:web`

Run: `npm run build:web`

Run: `bash deploy/agent/package_test.sh`

Run: `bash deploy/agent/install_test.sh`

Run: `node --test apps/web/src/deploy-polkit.test.js`

Run: `npm run test:e2e`

Expected: every command exits 0; Web tests contain no unhandled `act` warnings; all visible product copy is Chinese.

- [ ] **Step 10: Review the branch diff and commit**

Run: `git diff --check main...HEAD`

Run: `git status --short`

Expected: no whitespace errors and only intended files before the final commit.

```bash
git add apps/web/src tests/integration deploy README.md
git commit -m "feat: complete agent upgrade management console"
```

---

## Final Review Gate

After all 13 tasks are committed:

1. Read the confirmed spec and map every completion criterion to code and a passing test.
2. Review migration rollback, target compare-and-set transitions, duplicate WebSocket commands, process restart recovery, and failed-batch rollback boundaries.
3. Review all new Web states at desktop and mobile widths and verify keyboard-only operation.
4. Confirm the root upgrade unit can only apply fixed command IDs under `/var/lib/yunling-agent/upgrades`.
5. Confirm no SSH passwords, tokens, agent credentials, raw installation packages, or internal stack traces were committed.
6. Run the complete verification suite again from a clean working tree.
7. Report any Linux-only validation that cannot run on Windows separately; do not describe it as passed without evidence.
