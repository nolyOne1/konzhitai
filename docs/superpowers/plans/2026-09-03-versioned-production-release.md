# 云令版本化构建与人工批准生产发布 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为云令建立 `main` 成功后自动生成不可变候选镜像、人工批准发布到腾讯云、健康失败自动回滚并发送飞书通知的完整生产发布链路。

**Architecture:** 新增 `internal/release` 作为不依赖 GitHub 或终端界面的发布领域包，负责严格清单、兼容性摘要、生产状态、Compose 覆盖、预检、健康检查和事务式回滚；`cmd/yunling-release` 同时提供 Runner 侧清单工具和生产机固定入口。两个独立 GitHub 工作流分别发布 GHCR 候选和审批后通过受限 SSH 触发生产程序，代理发布内容迁入独立只读卷，普通控制面镜像不再携带代理二进制。

**Tech Stack:** Go 1.27、Docker Buildx、Docker Compose v2、GitHub Actions、GHCR、GitHub Environments、OpenSSH、飞书 V2 自定义机器人、现有 Go 集成测试。

**Spec:** `docs/superpowers/specs/2026-09-03-versioned-production-release-design.md`

## Global Constraints

- 首版只更新 `api`、`scheduler`、`web`、`ops`，不得更新 PostgreSQL、Redis、MinIO、Caddy；常规 `deploy`/`rollback` 路径不得运行 `bootstrap` 或数据库迁移，`bootstrap` 只允许在首次安装时由 root 从当前生产状态导入一次。
- 候选来自本仓库 `main` 的成功 `push` 型“云令 CI”运行；Pull Request、外部仓库、失败和取消运行不得发布候选。
- 生产只接受 `ghcr.io/nolyone1/yunling-{services,web,ops}@sha256:<64位小写摘要>`，不得接受标签或任意仓库。
- 生产发布必须引用 GitHub `production` 环境，审批前不得读取 SSH 或飞书环境 Secrets。
- 生产机允许 30–60 秒应用中断；健康检查总期限 2 分钟、间隔 5 秒，失败后不重试候选而直接回滚。
- 磁盘可用空间下限 3 GiB，`MemAvailable` 下限 512 MiB；不足时必须在容器变化前失败。
- 代理版本保持 `0.1.0`，现有两个安装包字节数与 SHA-256 必须保持不变；普通控制面候选不得包含代理二进制或代理包。
- Actions Artifact 保存 90 天；GHCR 历史镜像首版不自动删除。
- 所有 Action 固定到 40 位提交 SHA；所有 Dockerfile `FROM` 固定到镜像 SHA-256 摘要。
- 所有用户可见工作流名称、步骤、错误、通知和操作文档使用中文。
- 任何生产读取、GitHub 设置修改、Secrets 写入、腾讯云安装、生产发布或合并操作都在执行当时单独取得用户确认。

## Execution Setup

- 开始实现前，使用 `superpowers:using-git-worktrees` 从当前设计分支创建隔离工作树和分支 `codex/versioned-production-release`；不得直接在 `main` 或本计划分支上实施。
- 逐任务执行时使用 `superpowers:subagent-driven-development`（推荐）或在独立会话使用 `superpowers:executing-plans`，每个任务必须完成红灯、最小实现、绿灯、提交四个阶段。
- 声称任何任务或阶段完成前使用 `superpowers:verification-before-completion`，以本计划列出的命令和新鲜输出为证据。

## File Structure

- `internal/release/json.go`：拒绝重复键和未知字段的严格 JSON 解码。
- `internal/release/manifest.go`：候选清单、镜像白名单和请求格式。
- `internal/release/digest.go`：迁移树、部署契约和普通文件 SHA-256。
- `internal/release/agentlock.go`：代理锁文件与只读代理目录校验。
- `internal/release/state.go`：`current`、`previous`、历史清单、审计和原子写入。
- `internal/release/compose.go`：只生成四个应用服务镜像字段的 Compose 覆盖。
- `internal/release/host.go`：命令、资源、锁和 HTTP 探测接口。
- `internal/release/host_linux.go`：Linux 真实命令、`flock`、磁盘和内存实现。
- `internal/release/health.go`：四个容器、三个内部接口和两个公网接口检查。
- `internal/release/deployer.go`：预检、部署、自动回滚与人工回滚状态机。
- `internal/release/notify.go`：从机器结果生成并发送中文飞书发布卡片。
- `internal/release/sourcepolicy.go`：解析 Dockerfile 与 `.dockerignore`，执行可复用的构建来源安全策略。
- `internal/release/workflowpolicy.go`：语义解析 Actions YAML，验证触发器、权限、审批、并发和 Secrets 边界。
- `internal/release/github.go`：验证 GitHub 运行元数据与候选来源，不让信任判断藏在工作流内联 Shell 中。
- `cmd/yunling-release/main.go`：`manifest`、`request`、`execute`、`bootstrap`、`preflight`、`notify` 子命令。
- `deploy/agent/release-lock.json`：当前生产代理 `0.1.0` 的公开元数据锁，不保存二进制。
- `deploy/release/install.sh`：一次性安装受限账号、固定程序、sudoers 和初始基线。
- `deploy/release/install_test.sh`：使用假系统命令验证安装器失败关闭和权限边界。
- `deploy/release/testdata/`：实际 Docker 更新与回滚演练所需的隔离 Compose 配置。
- `.github/workflows/publish-candidate.yml`：成功 `main` CI 后构建、证明并保存候选。
- `.github/workflows/deploy-production.yml`：候选预检、环境审批、SSH 发布和飞书通知。
- `tests/integration/release_workflow_test.go`：两个工作流的静态安全契约。
- `tests/integration/release_deployment_test.go`：Dockerfile、Compose、安装器和发布边界契约。
- `tests/releaseintegration/release_transaction_test.go`：真实 Docker Compose 成功更新与失败回滚。
- `deploy/RELEASE.md`：全中文日常发布、回滚和排障手册。

---

### Task 1: 严格候选清单与兼容性摘要

**Files:**
- Create: `internal/release/json.go`
- Create: `internal/release/manifest.go`
- Create: `internal/release/manifest_test.go`
- Create: `internal/release/digest.go`
- Create: `internal/release/digest_test.go`

**Interfaces:**
- Produces: `DecodeManifest(io.Reader) (Manifest, error)`、`ValidateManifest(Manifest, ManifestPolicy) error`。
- Produces: `MigrationTreeDigest(string) (string, error)`、`DeploymentContractDigest([]string) (string, error)`、`FileSHA256(string) (string, error)`。
- Produces: `Manifest`、`Images`、`Compatibility`、`ManifestPolicy`，供候选工作流、生产预检和状态存储使用。

- [ ] **Step 1: 写严格解码和白名单失败测试**

```go
func TestDecodeManifestRejectsDuplicateAndUnknownFields(t *testing.T) {
	duplicate := `{"schema_version":1,"candidate_run_id":7,"candidate_run_id":8}`
	if _, err := DecodeManifest(strings.NewReader(duplicate)); err == nil {
		t.Fatal("重复 JSON 键必须失败")
	}
	unknown := validManifestJSON(`,"unexpected":true`)
	if _, err := DecodeManifest(strings.NewReader(unknown)); err == nil {
		t.Fatal("未知字段必须失败")
	}
}

func TestValidateManifestAcceptsOnlyPinnedYunlingImages(t *testing.T) {
	manifest := validManifest()
	policy := ManifestPolicy{RepositoryID: 42, Owner: "nolyone1"}
	if err := ValidateManifest(manifest, policy); err != nil {
		t.Fatalf("合法清单被拒绝：%v", err)
	}
	manifest.Images.Web = "ghcr.io/other/yunling-web@sha256:" + strings.Repeat("a", 64)
	if err := ValidateManifest(manifest, policy); err == nil {
		t.Fatal("非允许仓库必须失败")
	}
}
```

- [ ] **Step 2: 运行测试确认缺少实现**

Run: `go test ./internal/release -run 'TestDecodeManifest|TestValidateManifest' -count=1`

Expected: FAIL，提示 `DecodeManifest`、`ManifestPolicy` 等符号不存在。

- [ ] **Step 3: 定义清单和策略接口**

```go
const ManifestSchemaVersion = 1

type Images struct {
	Services string `json:"services"`
	Web      string `json:"web"`
	Ops      string `json:"ops"`
}

type Compatibility struct {
	MigrationTreeSHA256     string `json:"migration_tree_sha256"`
	DeploymentContractSHA256 string `json:"deployment_contract_sha256"`
	AgentVersion             string `json:"agent_version"`
	AgentManifestSHA256      string `json:"agent_manifest_sha256"`
}

type Manifest struct {
	SchemaVersion  int           `json:"schema_version"`
	CandidateRunID int64         `json:"candidate_run_id"`
	RepositoryID   int64         `json:"repository_id"`
	SourceSHA      string        `json:"source_sha"`
	CreatedAt      time.Time     `json:"created_at"`
	Images         Images        `json:"images"`
	Compatibility  Compatibility `json:"compatibility"`
}

type ManifestPolicy struct {
	RepositoryID int64
	Owner        string
}
```

Implement `DecodeManifest` with a token walk that records object keys at every nesting level before a second `json.Decoder` pass with `DisallowUnknownFields`; require exactly one JSON value and EOF. `ValidateManifest` must validate positive IDs, UTC RFC3339 time, 40 lowercase hex source SHA, exact three GHCR names, and `sha256:` plus 64 lowercase hex digits.

- [ ] **Step 4: 写规范化树摘要测试**

```go
func TestMigrationTreeDigestIsOrderedAndContentSensitive(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "000002.up.sql", "select 2;\n")
	writeFile(t, root, "000001.up.sql", "select 1;\n")
	first, err := MigrationTreeDigest(root)
	if err != nil { t.Fatal(err) }
	writeFile(t, root, "000001.up.sql", "select 3;\n")
	second, err := MigrationTreeDigest(root)
	if err != nil { t.Fatal(err) }
	if first == second { t.Fatal("内容改变必须改变迁移树摘要") }
}
```

- [ ] **Step 5: 实现摘要函数并运行包测试**

Canonical input for a tree digest must be, for each regular file in bytewise-sorted slash-normalized relative path order:

```text
<64位文件sha256><两个空格><相对路径><换行>
```

Hash the concatenation once more with SHA-256. Reject symlinks, non-regular files, absolute paths, `..`, duplicate normalized paths and an empty migration directory.

Run: `go test ./internal/release -count=1`

Expected: PASS。

- [ ] **Step 6: 提交清单核心**

```bash
git add internal/release/json.go internal/release/manifest.go internal/release/manifest_test.go internal/release/digest.go internal/release/digest_test.go
git commit -m "feat: 建立严格生产发布清单"
```

### Task 2: 锁定并隔离当前代理发布内容

**Files:**
- Create: `internal/release/agentlock.go`
- Create: `internal/release/agentlock_test.go`
- Create: `deploy/agent/release-lock.json`
- Modify: `deploy/Dockerfile.services`
- Modify: `deploy/docker-compose.yml`
- Modify: `deploy/agent/package_test.sh`
- Modify: `tests/integration/deployment_security_test.go`

**Interfaces:**
- Consumes: `FileSHA256(string)` from Task 1。
- Produces: `LoadAgentLock(string) (AgentLock, error)`、`VerifyAgentReleaseDir(AgentLock, string) error`。
- Produces: Compose named volume `yunling_agent_releases` mounted read-only at `/opt/yunling/releases/agent`。

- [ ] **Step 1: 写代理锁校验失败测试**

```go
func TestVerifyAgentReleaseDirAcceptsRecordedProductionFiles(t *testing.T) {
	root := t.TempDir()
	lock, manifest := testAgentLock(t, root)
	if err := VerifyAgentReleaseDir(lock, root); err != nil {
		t.Fatalf("合法代理目录被拒绝：%v", err)
	}
	if err := os.WriteFile(filepath.Join(root, manifest.Artifacts[0].FileName), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyAgentReleaseDir(lock, root); err == nil {
		t.Fatal("代理包漂移必须失败")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/release -run TestVerifyAgentReleaseDir -count=1`

Expected: FAIL，提示 `AgentLock` 或 `VerifyAgentReleaseDir` 不存在。

- [ ] **Step 3: 实现锁文件类型与目录校验**

```go
type AgentArtifactLock struct {
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	FileName string `json:"file_name"`
	ByteSize int64  `json:"byte_size"`
	SHA256   string `json:"sha256"`
}

type AgentLock struct {
	SchemaVersion  int                 `json:"schema_version"`
	Version        string              `json:"version"`
	ManifestSHA256 string              `json:"manifest_sha256"`
	Artifacts      []AgentArtifactLock `json:"artifacts"`
}
```

Require exactly Linux amd64 and arm64, fixed filenames derived from version, positive sizes, ordinary non-symlink files, exact byte counts and hashes. Verify raw `manifest.json` SHA-256 and parse it with the existing `internal/agentrelease` catalog rules so the lock and served manifest describe the same files.

- [ ] **Step 4: 写入已记录的生产代理锁**

```json
{
  "schema_version": 1,
  "version": "0.1.0",
  "manifest_sha256": "c80466d68f50e4ed25a2e8f32280df0768fba7306a43165874dc3996391cd1a7",
  "artifacts": [
    {
      "os": "linux",
      "arch": "amd64",
      "file_name": "yunling-agent-0.1.0-linux-amd64.tar.gz",
      "byte_size": 3424904,
      "sha256": "64982ac930917ac9a90e4a118d05214bee2ac421cee8d853e7e821d8acbcf3e4"
    },
    {
      "os": "linux",
      "arch": "arm64",
      "file_name": "yunling-agent-0.1.0-linux-arm64.tar.gz",
      "byte_size": 3066090,
      "sha256": "fdfb55e5936a7ee5ab4529734a05b35267f1735346aca4aa78aabb9a2d041fd1"
    }
  ]
}
```

Before committing, perform the separately approved read-only verification:

```bash
curl --fail --silent --show-error https://aiwise.top/api/releases/agent/latest -o "$TMPDIR/yunling-agent-manifest.json"
sha256sum "$TMPDIR/yunling-agent-manifest.json"
```

Expected manifest hash: `c80466d68f50e4ed25a2e8f32280df0768fba7306a43165874dc3996391cd1a7`; each downloaded package must match the two exact sizes and hashes above. Stop if any value differs and amend the spec with the observed production values before implementation continues.

- [ ] **Step 5: 改写服务镜像和 Compose 契约测试**

Replace `TestAPIDeploymentUsesEmbeddedReadOnlyAgentReleases` with assertions that the API has exactly:

```yaml
volumes:
  - yunling_api_secrets:/run/secrets:ro
  - yunling_agent_releases:/opt/yunling/releases/agent:ro
```

Assert the top-level volume exists, the mount is read-only, and `deploy/Dockerfile.services` contains none of `yunling-agent`, `agent-releases`, `AGENT_VERSION` or `deploy/agent/package.sh`.

- [ ] **Step 6: 运行测试确认旧镜像布局失败**

Run: `go test ./tests/integration -run 'TestAPIDeployment|TestServiceImage' -count=1`

Expected: FAIL because the current image embeds agent artifacts and Compose has no agent volume.

- [ ] **Step 7: 最小修改 Dockerfile、Compose 与 Shell 契约**

Remove all agent builds and copies from `deploy/Dockerfile.services`; keep API, Scheduler and Bootstrap. Add the named volume and API read-only mount. Change `deploy/agent/package_test.sh` to continue testing `package.sh` contents while asserting that service images do not package or copy agent files; the existing CI agent Job remains the only ordinary agent build in this phase.

- [ ] **Step 8: 运行代理、部署与 API 回归**

Run: `sh deploy/agent/package_test.sh`

Expected: PASS。

Run: `go test ./internal/release ./internal/agentrelease ./tests/integration -count=1`

Expected: PASS。

- [ ] **Step 9: 提交代理隔离**

```bash
git add internal/release/agentlock.go internal/release/agentlock_test.go deploy/agent/release-lock.json deploy/Dockerfile.services deploy/docker-compose.yml deploy/agent/package_test.sh tests/integration/deployment_security_test.go
git commit -m "feat: 隔离并锁定代理发布内容"
```

### Task 3: 原子生产状态与 Compose 覆盖

**Files:**
- Create: `internal/release/state.go`
- Create: `internal/release/state_test.go`
- Create: `internal/release/compose.go`
- Create: `internal/release/compose_test.go`

**Interfaces:**
- Consumes: `Manifest` and `Compatibility` from Task 1。
- Produces: `StoredRelease` with per-service `API`、`Scheduler`、`Web`、`Ops` image references。
- Produces: `StateStore.LoadCurrent()`、`LoadTarget(string)`、`SaveValidated(StoredRelease)`、`CommitSuccess(StoredRelease)`、`AppendAudit(AuditEvent)`。
- Produces: `RenderComposeOverride(StoredRelease) ([]byte, error)`。

- [ ] **Step 1: 写状态原子性和本地基线来源测试**

```go
func TestCommitSuccessRotatesCurrentAndPreviousAtomically(t *testing.T) {
	store := NewStateStore(t.TempDir())
	first := ghcrRelease(101)
	second := ghcrRelease(102)
	if err := store.CommitSuccess(first); err != nil { t.Fatal(err) }
	if err := store.CommitSuccess(second); err != nil { t.Fatal(err) }
	current, _ := store.LoadCurrent()
	previous, _ := store.LoadPrevious()
	if current.TargetID != "102" || previous.TargetID != "101" {
		t.Fatalf("状态轮换错误：current=%s previous=%s", current.TargetID, previous.TargetID)
	}
}

func TestRemoteManifestCannotCreateBootstrapOrigin(t *testing.T) {
	store := NewStateStore(t.TempDir())
	err := store.SaveValidated(StoredRelease{TargetID: "bootstrap", Origin: OriginBootstrap})
	if !errors.Is(err, ErrInvalidRelease) { t.Fatalf("实际错误：%v", err) }
}
```

- [ ] **Step 2: 运行状态测试确认失败**

Run: `go test ./internal/release -run 'TestCommitSuccess|TestRemoteManifest' -count=1`

Expected: FAIL，状态类型尚不存在。

- [ ] **Step 3: 实现状态类型和原子写入**

```go
type ReleaseOrigin string
const (
	OriginGHCR      ReleaseOrigin = "ghcr"
	OriginBootstrap ReleaseOrigin = "local-bootstrap"
)

type ServiceImages struct {
	API       string `json:"api"`
	Scheduler string `json:"scheduler"`
	Web       string `json:"web"`
	Ops       string `json:"ops"`
}

type StoredRelease struct {
	TargetID      string          `json:"target_id"`
	Origin        ReleaseOrigin   `json:"origin"`
	SourceSHA     string          `json:"source_sha"`
	Images        ServiceImages   `json:"images"`
	Compatibility Compatibility   `json:"compatibility"`
	SuccessfulAt  time.Time       `json:"successful_at"`
}
```

`SaveValidated` must only accept numeric GHCR targets created from a validated candidate. A separate root-only `CreateBootstrap` method accepts four distinct local image tags. Write JSON with mode `0600` to a sibling temporary file, call `Sync`, close, rename, then sync the parent directory. History files are create-exclusive and cannot be overwritten.

- [ ] **Step 4: 写 Compose 白名单测试**

```go
func TestRenderComposeOverrideContainsOnlyFourImageFields(t *testing.T) {
	release := ghcrRelease(101)
	content, err := RenderComposeOverride(release)
	if err != nil { t.Fatal(err) }
	want := "services:\n  api:\n    image: " + release.Images.API + "\n" +
		"  scheduler:\n    image: " + release.Images.Scheduler + "\n" +
		"  web:\n    image: " + release.Images.Web + "\n" +
		"  ops:\n    image: " + release.Images.Ops + "\n"
	if string(content) != want { t.Fatalf("覆盖文件不匹配：\n%s", content) }
}
```

- [ ] **Step 5: 实现确定性 Compose 输出并运行测试**

For GHCR candidates, map `manifest.Images.Services` to both API and Scheduler. For bootstrap, preserve four separately captured local tags. Quote no user data; image references have already passed the strict character whitelist.

Run: `go test ./internal/release -count=1`

Expected: PASS。

- [ ] **Step 6: 提交状态层**

```bash
git add internal/release/state.go internal/release/state_test.go internal/release/compose.go internal/release/compose_test.go
git commit -m "feat: 建立原子发布状态与覆盖配置"
```

### Task 4: Linux 预检与多层健康检查

**Files:**
- Create: `internal/release/host.go`
- Create: `internal/release/host_linux.go`
- Create: `internal/release/host_other.go`
- Create: `internal/release/host_test.go`
- Create: `internal/release/health.go`
- Create: `internal/release/health_test.go`

**Interfaces:**
- Produces: `CommandRunner.Run(context.Context, string, []string, []byte) (CommandResult, error)`。
- Produces: `ResourceReader.FreeBytes(string) (uint64, error)` and `AvailableMemory() (uint64, error)`。
- Produces: `Locker.TryLock(string) (func() error, error)`。
- Produces: `HealthChecker.CheckOnce(context.Context) error` and `Wait(context.Context, time.Duration, time.Duration) error`。

```go
type HealthChecker interface {
	CheckOnce(context.Context) error
	Wait(context.Context, time.Duration, time.Duration) error
}
```

- [ ] **Step 1: 写资源、命令和 TLS 探测测试**

```go
func TestPreflightStopsBeforeDockerWhenResourcesAreLow(t *testing.T) {
	runner := &recordingRunner{}
	resources := fakeResources{free: 3<<30 - 1, memory: 2 << 30}
	err := Preflight(context.Background(), HostConfig{}, runner, resources)
	if !errors.Is(err, ErrInsufficientDisk) { t.Fatalf("实际错误：%v", err) }
	if len(runner.Calls) != 0 { t.Fatal("资源不足时不得调用 Docker") }
}

func TestPublicHealthDoesNotDisableTLSVerification(t *testing.T) {
	checker := testHealthChecker(t)
	if err := checker.CheckOnce(context.Background()); err != nil { t.Fatal(err) }
	if checker.Client.Transport.(*http.Transport).TLSClientConfig.InsecureSkipVerify {
		t.Fatal("公网检查不得关闭 TLS 校验")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/release -run 'TestPreflight|TestPublicHealth' -count=1`

Expected: FAIL，主机接口和健康检查器尚不存在。

- [ ] **Step 3: 实现可替换主机接口与 Linux 适配器**

```go
type CommandResult struct {
	Stdout  []byte
	Stderr  []byte
	ExitCode int
}

type HostConfig struct {
	RootDir       string
	ComposeFile   string
	OverrideFile  string
	EnvFile       string
	ProjectName   string
	PublicBaseURL string
	MinFreeBytes  uint64
	MinMemory     uint64
}
```

Linux implementation uses `unix.Statfs`, parses only the `MemAvailable:` line from `/proc/meminfo`, and uses non-blocking `unix.Flock`. The non-Linux file returns `ErrUnsupportedPlatform` so Windows unit tests compile without pretending production support.

- [ ] **Step 4: 实现固定预检顺序**

Run these checks only after resources pass: `docker version`、`docker compose version`、then `docker compose ... ps --format json` for exactly `postgres redis minio caddy`. Reject missing, stopped, starting or unhealthy infrastructure. Do not run `up`、`restart`、`down` or any migration command in preflight.

- [ ] **Step 5: 实现健康检查与边界**

Container checks read `docker inspect --format {{json .State.Health.Status}}` for `yunling-api-1`、`yunling-scheduler-1`、`yunling-web-1`、`yunling-ops-1`. Internal checks use `docker exec` with fixed arguments for API `/api/health`、Web `/healthz` and Ops `/healthz`. Public checks use an `http.Client` with 5-second timeout, redirects disabled, default certificate verification, 64 KiB response limit, and exact paths `/healthz` and `/api/health`.

- [ ] **Step 6: 运行预检和健康测试**

Run: `go test ./internal/release -run 'TestPreflight|TestHealth|TestPublicHealth' -count=1`

Expected: PASS。

- [ ] **Step 7: 提交主机检查**

```bash
git add internal/release/host.go internal/release/host_linux.go internal/release/host_other.go internal/release/host_test.go internal/release/health.go internal/release/health_test.go
git commit -m "feat: 增加生产预检与健康检查"
```

### Task 5: 事务式部署、自动回滚与人工回滚

**Files:**
- Create: `internal/release/deployer.go`
- Create: `internal/release/deployer_test.go`

**Interfaces:**
- Consumes: `Manifest`、`StateStore`、`RenderComposeOverride`、`CommandRunner`、`ResourceReader`、`Locker`、`HealthChecker`。
- Produces: `Deployer.Execute(context.Context, Request) (Result, error)`。
- Produces: stable JSON `Request` and `Result` contracts for the CLI and GitHub workflow。

- [ ] **Step 1: 写请求、成功部署和失败回滚测试**

```go
type Operation string
const (
	OperationDeploy   Operation = "deploy"
	OperationRollback Operation = "rollback"
)

type Request struct {
	Operation     Operation `json:"operation"`
	TargetID      string    `json:"target_id"`
	Actor         string    `json:"actor"`
	WorkflowRunID int64     `json:"workflow_run_id"`
	WorkflowURL   string    `json:"workflow_url"`
	Manifest      *Manifest `json:"manifest,omitempty"`
}

type Result struct {
	Operation       Operation `json:"operation"`
	TargetID        string    `json:"target_id"`
	SourceSHA       string    `json:"source_sha,omitempty"`
	Status          string    `json:"status"`
	RollbackStatus  string    `json:"rollback_status,omitempty"`
	DiagnosticID    string    `json:"diagnostic_id,omitempty"`
	StartedAt       time.Time `json:"started_at"`
	FinishedAt      time.Time `json:"finished_at"`
}
```

`Result.Status` 只允许 `succeeded` 或 `failed`；`RollbackStatus` 只允许空值、`not-required`、`succeeded` 或 `failed`。失败结果必须带非空 `DiagnosticID`，但不得把诊断正文写入标准输出。

Test exact command order: three `docker pull` calls, three digest inspections, atomic override write, one Compose `up -d --no-deps --no-build api scheduler web ops`, health wait, then state commit. A failed post-update health check must render the saved previous release, run the same Compose command once, and health-check the rollback.

- [ ] **Step 2: 运行事务测试确认失败**

Run: `go test ./internal/release -run 'TestDeploy|TestRollback' -count=1`

Expected: FAIL，`Deployer` 尚不存在。

- [ ] **Step 3: 实现发布状态机**

```go
type Deployer struct {
	Config    HostConfig
	Policy    ManifestPolicy
	Store     *StateStore
	Runner    CommandRunner
	Resources ResourceReader
	Locker    Locker
	Health    HealthChecker
	Now       func() time.Time
}

func (d *Deployer) Execute(ctx context.Context, request Request) (Result, error)
```

Validate operation/target/actor/workflow URL first; acquire lock second. For `deploy`, require a manifest whose run ID equals numeric target, then compare all three compatibility hashes with current. For `rollback`, reject a manifest and load only a local successful history target. Preflight and pull happen before writing the override. Store diagnostic logs root-only and cap each service at 200 lines plus an aggregate 1 MiB.

- [ ] **Step 4: 覆盖所有失败边界**

Add table tests for lock contention, invalid target, incompatible migration/deployment/agent hash, low resources, unhealthy infrastructure, pull failure, digest mismatch, each app unhealthy, public health failure, rollback success, rollback failure and rejected unknown history. Assert no `docker compose down`、volume command、bootstrap or migration string ever reaches the runner.

- [ ] **Step 5: 运行发布包测试**

Run: `go test ./internal/release -count=1`

Expected: PASS。

- [ ] **Step 6: 提交发布状态机**

```bash
git add internal/release/deployer.go internal/release/deployer_test.go
git commit -m "feat: 实现生产发布与自动回滚事务"
```

### Task 6: 固定发布 CLI 与飞书结果通知

**Files:**
- Create: `cmd/yunling-release/main.go`
- Create: `cmd/yunling-release/main_test.go`
- Create: `internal/release/notify.go`
- Create: `internal/release/notify_test.go`
- Modify: `Makefile`

**Interfaces:**
- Consumes: all `internal/release` interfaces from Tasks 1–5 and existing `notification.SignFeishu` / `notification.ValidateFeishuWebhook` security rules。
- Produces commands: `manifest create`、`manifest validate`、`request create`、`execute`、`preflight`、`notify`；Task 7 再加入只供首次生产初始化使用的 `bootstrap`。
- Produces: `Notifier.Send(context.Context, webhook, signingSecret string, Result) error`。

- [ ] **Step 1: 写子命令和标准输出安全测试**

```go
func TestExecuteReadsOneStrictRequestAndWritesOneResult(t *testing.T) {
	stdin := strings.NewReader(validExecuteRequestJSON())
	stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
	code := run([]string{"execute"}, stdin, stdout, stderr, fakeDependencies())
	if code != 0 { t.Fatalf("退出码=%d stderr=%s", code, stderr) }
	var result release.Result
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil { t.Fatal(err) }
	if strings.Contains(stdout.String(), "secret") { t.Fatal("输出泄露测试秘密") }
}
```

Add argument tests proving `execute` refuses non-root real mode, reads at most 256 KiB, rejects trailing JSON, and never reads free-form `SSH_ORIGINAL_COMMAND`.

- [ ] **Step 2: 运行 CLI 测试确认失败**

Run: `go test ./cmd/yunling-release -count=1`

Expected: FAIL，命令入口尚不存在。

- [ ] **Step 3: 实现命令分派和清单生成**

`manifest create` accepts explicit flags for run ID, repository ID, source SHA, three image references, repository root, agent lock and output path. It computes migration and deployment contract hashes itself and writes mode `0600`. `manifest validate` performs syntax and repository checks only; production兼容性仍由服务器当前状态决定。`request create` emits one strict request object and requires a manifest only for `deploy`.

`execute` constructs real Linux dependencies with fixed `/opt/yunling` paths and emits exactly one `Result` JSON object. Human messages go to stderr in Chinese; secrets and full command environments never do.

- [ ] **Step 4: 写飞书签名、卡片和跳转限制测试**

```go
func TestReleaseNotifierUsesChineseCardAndGitHubRunLink(t *testing.T) {
	var received []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil { t.Fatal(err) }
		received = append([]byte(nil), body...)
		for _, required := range []string{"生产发布失败", "自动回滚成功", "github.com/nolyOne1/konzhitai/actions/runs/"} {
			if !bytes.Contains(body, []byte(required)) { t.Errorf("缺少 %q", required) }
		}
		_, _ = io.WriteString(w, `{"code":0}`)
	}))
	defer server.Close()

	target, err := url.Parse(server.URL)
	if err != nil { t.Fatal(err) }
	client := server.Client()
	client.Transport = rewriteFeishuTransport{target: target, base: client.Transport}
	notifier := NewNotifier(client, func() time.Time {
		return time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	})
	result := failedReleaseResult("https://github.com/nolyOne1/konzhitai/actions/runs/123")
	err = notifier.Send(context.Background(), "https://open.feishu.cn/open-apis/bot/v2/hook/00000000-0000-4000-8000-000000000000", "test-signing-secret", result)
	if err != nil { t.Fatal(err) }
	if bytes.Contains(received, []byte("test-signing-secret")) { t.Fatal("签名密钥不得进入卡片正文") }
}

type rewriteFeishuTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (r rewriteFeishuTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.URL.Scheme = r.target.Scheme
	clone.URL.Host = r.target.Host
	return r.base.RoundTrip(clone)
}
```

- [ ] **Step 5: 实现发布专用飞书通知**

Reuse the existing HMAC signing function and strict `open.feishu.cn/open-apis/bot/v2/hook/<uuid>` validator. Allow only `https://github.com/nolyOne1/konzhitai/actions/runs/<digits>` as workflow link. Card contains operation, actor, target, source short SHA, status, rollback status, diagnostic ID and Shanghai-formatted time; it never contains full webhook, signing secret, SSH data or environment dump.

- [ ] **Step 6: 添加构建目标并运行测试**

Add `go build -o bin/yunling-release ./cmd/yunling-release` to `make build` and a dedicated `build-release` target producing `bin/yunling-release-linux-amd64` with `CGO_ENABLED=0 GOOS=linux GOARCH=amd64`.

Run: `go test ./internal/release ./cmd/yunling-release -count=1`

Expected: PASS。

Run: `make build-release`

Expected: `bin/yunling-release-linux-amd64` exists and `file` reports a Linux x86-64 executable.

- [ ] **Step 7: 提交 CLI 和通知**

```bash
git add cmd/yunling-release internal/release/notify.go internal/release/notify_test.go Makefile
git commit -m "feat: 提供固定发布命令与飞书通知"
```

### Task 7: 受限账号安装与生产基线导入

**Files:**
- Create: `deploy/release/install.sh`
- Create: `deploy/release/install_test.sh`
- Create: `deploy/release/authorized-key-options.txt`
- Modify: `cmd/yunling-release/main.go`
- Modify: `cmd/yunling-release/main_test.go`
- Modify: `.gitattributes`

**Interfaces:**
- Consumes: release state、主机预检和 CLI 分派能力 from Tasks 1–6。
- Produces: `yunling-release bootstrap`，且该子命令只能由真实 root 在尚无初始化状态时执行一次。
- Produces: user `yunling-deploy`, forced authorized key, `/etc/sudoers.d/yunling-deploy`, root-owned `/usr/local/sbin/yunling-release`, `/opt/yunling/releases/bootstrap/release-manifest.json`。
- Produces: initialized Docker volume `yunling_agent_releases` with exact locked production artifacts。

- [ ] **Step 1: 写安装器失败关闭测试**

```sh
if YUNLING_INSTALL_ROOT="$test_root" YUNLING_SYSTEMCTL_BIN="$fake_systemctl" \
  sh "$root/deploy/release/install.sh" --public-key-file "$test_root/bad.pub"; then
  echo '非 Ed25519 公钥必须失败' >&2
  exit 1
fi
test ! -e "$test_root/etc/sudoers.d/yunling-deploy"
```

Also fake `useradd`、`install`、`visudo`、`docker` and `sshd -T`; assert a failure at any stage does not enable the authorized key or replace a working release binary.

- [ ] **Step 2: 运行 Shell 测试确认失败**

Run: `bash deploy/release/install_test.sh`

Expected: FAIL because the installer does not exist.

- [ ] **Step 3: 实现受限安装器**

The installer requires effective UID 0 and one ordinary `ssh-ed25519` public-key line from a mode `0600` root-owned file. Create a locked-password user with home `/var/lib/yunling-deploy` and shell `/bin/sh`. Write this exact key option prefix before the supplied public key:

```text
restrict,command="/usr/bin/sudo -n /usr/local/sbin/yunling-release execute"
```

Install sudoers content exactly:

```text
yunling-deploy ALL=(root) NOPASSWD: /usr/local/sbin/yunling-release execute
```

Validate a temporary sudoers file with `visudo -cf` before atomic rename. Authorized keys must be `0600`; its directory `0700`; binary and parent directories root-owned and not group/world-writable. Do not modify global `sshd_config`.

- [ ] **Step 4: 实现 bootstrap 基线导入**

`yunling-release bootstrap` must:

1. Resolve current container image IDs independently for `yunling-api-1`、`yunling-scheduler-1`、`yunling-web-1`、`yunling-ops-1`.
2. Tag each ID as `yunling-local-bootstrap/<service>:<12位镜像ID>` without merging API and Scheduler.
3. `docker cp` `/opt/yunling/releases/agent` from the current API container into a mode `0700` temporary directory.
4. Verify exact lock metadata and hashes before creating or changing the named volume.
5. Populate a newly created temporary volume, verify it through a read-only mount, then adopt it as `yunling_agent_releases`; refuse to overwrite a non-empty existing volume.
6. Create the `bootstrap` stored release and initial Compose override atomically.

- [ ] **Step 5: 运行安装与 bootstrap 测试**

Run: `bash deploy/release/install_test.sh`

Expected: PASS。

Run: `go test ./cmd/yunling-release ./internal/release -run 'TestBootstrap|TestInstall' -count=1`

Expected: PASS。

- [ ] **Step 6: 强制发布文件使用 LF 并提交**

Add `deploy/release/*.txt text eol=lf` to `.gitattributes`; existing `*.sh` rule covers scripts.

```bash
git add deploy/release cmd/yunling-release .gitattributes
git commit -m "feat: 增加受限生产发布安装器"
```

### Task 8: 固定基础镜像与收紧构建上下文

**Files:**
- Modify: `deploy/Dockerfile.services`
- Modify: `deploy/Dockerfile.web`
- Modify: `deploy/Dockerfile.ops`
- Modify: `deploy/Dockerfile.minio`
- Modify: `.dockerignore`
- Create: `internal/release/sourcepolicy.go`
- Create: `internal/release/sourcepolicy_test.go`
- Create: `deploy/release/testdata/context-probe.Dockerfile`
- Create: `tests/integration/release_deployment_test.go`

**Interfaces:**
- Produces: every `FROM` line in every `deploy/Dockerfile*` includes an immutable `@sha256:<64 lowercase hex>` digest。
- Produces: build contexts exclude credentials, release state, backups and local outputs。
- Produces: `ValidateBuildSources(repositoryRoot string) error`，供本地测试和 CI 对真实仓库执行同一策略。

- [ ] **Step 1: 写基础镜像和构建上下文策略的行为测试**

```go
func TestValidateBuildSourcesRejectsMovableBaseAndMissingSecretExclusion(t *testing.T) {
	root := newBuildPolicyFixture(t)
	writeFixture(t, root, "deploy/Dockerfile.services", "FROM alpine:3.24\n")
	writeFixture(t, root, ".dockerignore", "node_modules\n")
	err := ValidateBuildSources(root)
	if !errors.Is(err, ErrUnsafeBuildSource) {
		t.Fatalf("未固定基础镜像和缺少密钥排除规则必须失败：%v", err)
	}
}
```

Add a passing fixture with digest-pinned `FROM` entries and effective ignore rules for nested `.env`、`deploy/secrets`、`releases`、`backups`、`*.pem`、`id_rsa*`、`id_ed25519*` and build outputs. Expectations are literal fixtures; tests call the real policy and do not search the repository file as plain text.

- [ ] **Step 2: 运行契约测试确认失败**

Run: `go test ./internal/release -run TestValidateBuildSources -count=1`

Expected: FAIL because `ValidateBuildSources` does not exist.

- [ ] **Step 3: 从注册表解析并记录实际索引摘要**

Run on a Linux machine with Docker Buildx and network access:

```bash
for image in \
  golang:1.27.0-alpine3.24 alpine:3.24 node:24-alpine3.24 nginx:1.29.8-alpine \
  restic/restic:0.19.1 quay.io/minio/mc:RELEASE.2025-08-13T08-35-41Z postgres:18.6-alpine \
  golang:1.24.8-alpine3.22 alpine:3.22; do
  docker buildx imagetools inspect "$image" --format '{{json .Manifest.Digest}}'
done
```

Expected: nine quoted values matching `"sha256:[0-9a-f]{64}"`. Append each returned digest to its existing tag in every `FROM`, keeping the human-readable tag before `@sha256:`. Review the manifest platform list and reject any tag without `linux/amd64`.

- [ ] **Step 4: 收紧 `.dockerignore`、运行真实上下文探针并构建全部镜像**

Implement `ValidateBuildSources` with a Dockerfile parser that evaluates every stage and rejects any registry base without a digest. Parse ignore rules with Docker-compatible matching rather than substring checks.

Create temporary sentinel files under each sensitive path, then use `context-probe.Dockerfile` to `COPY . /context` into a local BuildKit output. Assert none of the sentinels exists in the exported context and always remove only those explicitly named temporary sentinels.

Run: `docker compose --env-file deploy/.env.example -f deploy/docker-compose.yml build web api scheduler ops bootstrap minio`

Expected: all builds succeed from pinned bases; services image contains no agent artifacts.

- [ ] **Step 5: 运行部署契约测试并提交**

Run: `go test ./internal/release ./tests/integration -run 'TestValidateBuildSources|TestAPIDeployment' -count=1`

Expected: PASS。

```bash
git add deploy/Dockerfile.services deploy/Dockerfile.web deploy/Dockerfile.ops deploy/Dockerfile.minio deploy/release/testdata/context-probe.Dockerfile .dockerignore internal/release/sourcepolicy.go internal/release/sourcepolicy_test.go tests/integration/release_deployment_test.go
git commit -m "build: 固定生产基础镜像摘要"
```

### Task 9: 自动候选构建、GHCR 推送与来源证明

**Files:**
- Create: `.github/workflows/publish-candidate.yml`
- Create: `internal/release/github.go`
- Create: `internal/release/github_test.go`
- Create: `internal/release/workflowpolicy.go`
- Create: `internal/release/workflowpolicy_test.go`
- Create: `tests/integration/release_workflow_test.go`
- Modify: `cmd/yunling-release/main.go`
- Modify: `cmd/yunling-release/main_test.go`

**Interfaces:**
- Consumes: `yunling-release manifest create/validate` from Task 6。
- Produces: workflow `云令候选版本` and artifact `yunling-release-<40位source SHA>` retained 90 days。
- Produces: three GHCR image digests and one bootstrap bundle for first installation。
- Produces: `ValidateCandidateRun(RunMetadata, CandidatePolicy) error` and `ValidateWorkflowFiles(repositoryRoot string) error`。

- [ ] **Step 1: 写候选来源与工作流策略行为测试**

```go
func TestValidateCandidateRunRejectsUntrustedRuns(t *testing.T) {
	valid := RunMetadata{Workflow: "云令 CI", Conclusion: "success", Branch: "main", Event: "push", RepositoryID: 42}
	cases := []struct {
		name string
		edit func(*RunMetadata)
	}{
		{"错误工作流", func(run *RunMetadata) { run.Workflow = "其他" }},
		{"失败结论", func(run *RunMetadata) { run.Conclusion = "failure" }},
		{"非主分支", func(run *RunMetadata) { run.Branch = "feature" }},
		{"非推送事件", func(run *RunMetadata) { run.Event = "pull_request" }},
		{"外部仓库", func(run *RunMetadata) { run.RepositoryID = 99 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run := valid
			tc.edit(&run)
			if err := ValidateCandidateRun(run, CandidatePolicy{RepositoryID: 42}); err == nil {
				t.Fatal("不受信任的运行必须失败")
			}
		})
	}
}
```

Add semantic YAML fixtures. `ValidateWorkflowFiles` must decode mappings/sequences and reject: candidate triggers other than completed `workflow_run` of `云令 CI`; production/SSH references in candidate; permissions broader than the exact allowlist; artifact retention other than 90; missing three attestations; mutable Action refs; any agent image publication. Mutate one semantic field per table row so each failure names a real security regression rather than a source-text change.

- [ ] **Step 2: 运行工作流测试确认失败**

Run: `go test ./internal/release -run 'TestValidateCandidateRun|TestValidateWorkflowFiles' -count=1`

Expected: FAIL because the run and workflow policy do not exist.

- [ ] **Step 3: 编写候选工作流信任门**

Use this outer structure and retain the exact guards. The first step serializes `github.event.workflow_run` into a bounded JSON file and calls the tested `yunling-release candidate authorize` command; the Job-level `if` remains a defense-in-depth guard:

```yaml
name: 云令候选版本
on:
  workflow_run:
    workflows: ["云令 CI"]
    types: [completed]
permissions:
  contents: read
concurrency:
  group: candidate-${{ github.event.workflow_run.head_sha }}
  cancel-in-progress: true
jobs:
  publish:
    if: >-
      github.event.workflow_run.conclusion == 'success' &&
      github.event.workflow_run.head_branch == 'main' &&
      github.event.workflow_run.event == 'push' &&
      github.event.workflow_run.repository.id == github.repository_id
    permissions:
      contents: read
      packages: write
      id-token: write
      attestations: write
```

Checkout exact `${{ github.event.workflow_run.head_sha }}` with `actions/checkout@fbc6f3992d24b796d5a048ff273f7fcc4a7b6c09`.

- [ ] **Step 4: 构建并推送三个镜像**

Login with `docker/login-action@f4ef78c080cd8ba55a85445d5b36e214a81df20a`. Use three named `docker/build-push-action@3b5e8027fcad23fda98b2e3ac259d8d67585f671` steps with `push: true`, `platforms: linux/amd64`, the exact Dockerfile, and tag `ghcr.io/nolyone1/yunling-<name>:sha-${{ github.event.workflow_run.head_sha }}`. Capture each `digest` output.

- [ ] **Step 5: 生成清单、证明与 bootstrap 包**

Run the static CLI to create and validate `release-manifest.json`, then attest each image with `actions/attest@59d89421af93a897026c735860bf21b6eb4f7b26`, `subject-name` without tag, `subject-digest` from the corresponding build output, and `push-to-registry: true`.

Build `yunling-release-linux-amd64`; package it with `deploy/release/install.sh`、`deploy/docker-compose.yml` and `deploy/agent/release-lock.json` into `yunling-release-bootstrap.tar.gz`, create `SHA256SUMS`, and attest the tarball. Upload the manifest, its `SHA256SUMS`, and bootstrap tarball using `actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02`, artifact name `yunling-release-${{ github.event.workflow_run.head_sha }}`, `retention-days: 90`, `if-no-files-found: error`.

- [ ] **Step 6: 运行 Action 语法、语义策略和本地清单生成**

Run: `actionlint .github/workflows/publish-candidate.yml`

Expected: exit 0 with no diagnostics.

Run: `go test ./internal/release ./tests/integration -run 'TestValidateCandidateRun|TestValidateWorkflowFiles|TestCandidateWorkflow' -count=1`

Expected: PASS。

Run: `go run ./cmd/yunling-release manifest create --candidate-run-id 123 --repository-id 456 --source-sha $(git rev-parse HEAD) --services ghcr.io/nolyone1/yunling-services@sha256:$(printf a%.0s {1..64}) --web ghcr.io/nolyone1/yunling-web@sha256:$(printf b%.0s {1..64}) --ops ghcr.io/nolyone1/yunling-ops@sha256:$(printf c%.0s {1..64}) --repository-root . --agent-lock deploy/agent/release-lock.json --output "$TMPDIR/release-manifest.json"`

Expected: command succeeds and `manifest validate` accepts the resulting file.

- [ ] **Step 7: 提交候选工作流**

```bash
git add .github/workflows/publish-candidate.yml internal/release/github.go internal/release/github_test.go internal/release/workflowpolicy.go internal/release/workflowpolicy_test.go tests/integration/release_workflow_test.go cmd/yunling-release
git commit -m "ci: 自动发布不可变候选镜像"
```

### Task 10: 人工批准生产工作流

**Files:**
- Create: `.github/workflows/deploy-production.yml`
- Modify: `tests/integration/release_workflow_test.go`

**Interfaces:**
- Consumes: candidate run ID/artifact from Task 9 and `request create`、`manifest validate`、`notify` from Task 6。
- Produces: manual operations `deploy` and `rollback`, production environment deployment, strict SSH request and Feishu final notification。

- [ ] **Step 1: 写生产审批、权限和输入策略的行为测试**

```go
func TestValidateProductionInputAllowsOnlyDeployOrHistoricalRollback(t *testing.T) {
	valid := []ProductionInput{
		{Operation: "deploy", TargetID: "123"},
		{Operation: "rollback", TargetID: "456"},
		{Operation: "rollback", TargetID: "bootstrap"},
	}
	for _, input := range valid {
		if err := ValidateProductionInput(input); err != nil { t.Fatalf("合法输入被拒绝：%v", err) }
	}
	for _, input := range []ProductionInput{{"deploy", "bootstrap"}, {"deploy", "01"}, {"shell", "123"}, {"rollback", "../current"}} {
		if err := ValidateProductionInput(input); err == nil { t.Fatalf("危险输入被接受：%+v", input) }
	}
}
```

Extend semantic `ValidateWorkflowFiles` fixtures to reject production secrets outside the Job declaring `environment.name=production`, non-manual triggers, missing or cancelable `production-release` concurrency, password SSH, disabled host-key verification, `curl -k`, or a non-fixed remote command. Test the generated SSH argument vector from `request create` directly and require `StrictHostKeyChecking=yes`、`BatchMode=yes` and `IdentitiesOnly=yes`.

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/release ./cmd/yunling-release -run 'TestValidateProductionInput|TestSSHArguments|TestValidateWorkflowFiles' -count=1`

Expected: FAIL because the production input and workflow policy are not implemented.

- [ ] **Step 3: 实现无密钥预检 Job**

Define `operation` as a choice and `target_id` as a required string. For deploy, require `^[1-9][0-9]{0,18}$`, query `/repos/${GITHUB_REPOSITORY}/actions/runs/${target_id}` with `gh api`, and require the exact candidate workflow, repository ID, `main`, `push`, `success` and head SHA. Download only `yunling-release-<head_sha>` from that run with `actions/download-artifact@634f93cb2916e3fdff6788551b99b062d0335ce0`, `github-token` and `run-id`. Validate upload digest, `SHA256SUMS`, manifest and image attestations. For rollback, only allow the numeric form or exact `bootstrap` and do not require an expired candidate artifact.

- [ ] **Step 4: 实现审批后 SSH 发布 Job**

Use:

```yaml
environment:
  name: production
  url: https://aiwise.top
concurrency:
  group: production-release
  cancel-in-progress: false
```

Write `PRODUCTION_SSH_PRIVATE_KEY` to a Runner temp file under `umask 077`; write `PRODUCTION_SSH_KNOWN_HOSTS` to a separate file. Build a strict request JSON, then call OpenSSH with fixed options and stdin redirection. The remote command string is the fixed word `execute`; the authorized key still forces the server-side command. Capture stdout in `result.json`, cap it at 64 KiB, validate it as exactly one `Result`, and treat SSH or result validation failure as a failed release.

- [ ] **Step 5: 实现不掩盖部署结论的飞书步骤**

Always invoke `yunling-release notify` after the SSH attempt using `PRODUCTION_FEISHU_WEBHOOK` and `PRODUCTION_FEISHU_SIGNING_SECRET`. Set only this notification step to `continue-on-error: true`; append `notification_failed` to `$GITHUB_STEP_SUMMARY` if it fails. After notification, exit with the saved deployment exit code so a failed or failed-rollback deployment remains red while a healthy deployment remains green.

- [ ] **Step 6: 运行 Action 语法、语义策略并提交**

Run: `actionlint .github/workflows/publish-candidate.yml .github/workflows/deploy-production.yml`

Expected: exit 0 with no diagnostics.

Run: `go test ./internal/release ./cmd/yunling-release ./tests/integration -run 'TestValidateProductionInput|TestSSHArguments|TestValidateWorkflowFiles|TestCandidateWorkflow|TestProductionWorkflow' -count=1`

Expected: PASS。

```bash
git add .github/workflows/deploy-production.yml tests/integration/release_workflow_test.go
git commit -m "ci: 增加人工批准生产发布"
```

### Task 11: 真实 Docker Compose 更新与回滚演练

**Files:**
- Create: `deploy/release/testdata/docker-compose.yml`
- Create: `deploy/release/testdata/nginx.conf`
- Create: `tests/releaseintegration/release_transaction_test.go`
- Modify: `.github/workflows/ci.yml`
- Modify: `tests/integration/ci_workflow_test.go`

**Interfaces:**
- Consumes: real `Deployer` with injected test policy and short health deadline。
- Produces: build-tagged test command `go test -tags=releaseintegration ./tests/releaseintegration -count=1`。

- [ ] **Step 1: 写隔离 Compose 测试骨架**

```go
//go:build releaseintegration

func TestRealComposeDeployAndRollbackPreserveInfrastructure(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil { t.Fatal("需要 Docker") }
	project := "yunling_release_test_" + strconv.Itoa(os.Getpid())
	startHarness(t, project)
	before := infrastructureContainerIDs(t, project)
	deployGoodRelease(t, project)
	deployBadWebReleaseAndRequireRollback(t, project)
	after := infrastructureContainerIDs(t, project)
	if !reflect.DeepEqual(before, after) { t.Fatalf("基础设施容器被替换：%v -> %v", before, after) }
}
```

- [ ] **Step 2: 运行测试确认缺少演练环境**

Run: `go test -tags=releaseintegration ./tests/releaseintegration -count=1`

Expected: FAIL because the test Compose and helpers do not exist.

- [ ] **Step 3: 建立固定镜像的隔离环境**

Use pinned nginx and alpine digests resolved in Task 8. Define four application services and four named infrastructure services with health checks; expose only a random localhost test port. The good target returns `ok`; the bad Web target exits immediately. Inject a test-only `ManifestPolicy` that accepts the exact pinned test registry names; production policy remains the hard-coded GHCR allowlist.

- [ ] **Step 4: 完成真实成功与失败路径**

Assert successful deployment updates all four app container IDs, saves current/previous and passes internal/public checks. Then deploy the bad Web target, require automatic rollback, assert app image IDs equal the previous successful target, and assert all four infrastructure IDs and named volumes are unchanged. Cleanup only the test project with its explicit generated name.

- [ ] **Step 5: 把演练加入现有“部署配置与镜像”检查**

Add one step after application image build:

```yaml
- name: 演练应用发布与自动回滚
  run: go test -tags=releaseintegration ./tests/releaseintegration -count=1
```

Update `ci_workflow_test.go` to require this exact command without changing the stable Job name `部署配置与镜像`.

- [ ] **Step 6: 运行真实演练和 CI 契约**

Run: `go test -tags=releaseintegration ./tests/releaseintegration -count=1`

Expected: PASS。

Run: `go test ./tests/integration -run 'TestCI|TestProductionWorkflow' -count=1`

Expected: PASS。

- [ ] **Step 7: 提交演练**

```bash
git add deploy/release/testdata tests/releaseintegration .github/workflows/ci.yml tests/integration/ci_workflow_test.go
git commit -m "test: 演练真实发布与自动回滚"
```

### Task 12: 中文发布文档与完整本地验收

**Files:**
- Create: `deploy/RELEASE.md`
- Modify: `deploy/README.md`
- Modify: `deploy/PRODUCTION.md`
- Modify: `README.md`

**Interfaces:**
- Consumes: all implemented commands, paths, settings and failure states。
- Produces: operator procedures for candidate selection, approval, rollback, diagnostics, key rotation and first bootstrap。

- [ ] **Step 1: 先生成可核验的命令与状态清单**

Run every implemented `yunling-release <command> --help` form and save its fresh stdout in the task notes. Enumerate the actual `Result.Status` / `RollbackStatus` values, root-only state paths, public health URLs and workflow input names from the compiled program and parsed workflow model. This is the independent source for the manual documentation review; do not copy expectations from a prose keyword test.

- [ ] **Step 2: 编写发布与排障手册**

Document these exact flows: locate successful candidate run; inspect source SHA/digests/attestations; approve or reject production; read result; manual rollback to numeric target or bootstrap; inspect root-only diagnostic ID; rotate the dedicated Ed25519 key; disable the authorized key; handle GHCR/SSH/health/rollback/Feishu failures. Replace the old daily `git pull` and `docker compose build` production update section with a link to `deploy/RELEASE.md`.

- [ ] **Step 3: 更新生产记录模板**

Add a “版本化发布” section to `deploy/PRODUCTION.md` with fields for current candidate run ID, source SHA, services/web/ops digests, previous target, first enable time, last rollback drill and agent lock hash. Do not invent deployment results before the real rollout; phrase it as the required record format and keep current historical facts unchanged.

- [ ] **Step 4: 人工走查中文文档**

The user approved this documentation-only exception to automated TDD on 2026-09-03. Using the fresh command/state checklist from Step 1, review each procedure line by line and require coverage of: 候选运行编号、`production` 环境审批、自动回滚、人工回滚、代理发布只读卷、禁止 `docker compose down -v`、禁止 `latest`、禁止关闭 SSH 主机指纹校验。Execute every read-only example and every `--help` command; for mutating examples, compare the exact command against the tested CLI parser without running it. Record the review checklist in the implementation commit message or Pull Request body.

- [ ] **Step 5: 运行完整本地验证**

Run: `gofmt -w internal/release/*.go cmd/yunling-release/*.go tests/integration/*.go tests/releaseintegration/*.go`

Run: `go test -race -p=1 ./... -count=1`

Expected: PASS。

Run: `npm run test:web`

Expected: all Web tests PASS。

Run: `npm run build:web`

Expected: production build PASS。

Run: `bash deploy/agent/package_test.sh && bash deploy/release/install_test.sh`

Expected: PASS。

Run: `docker compose --env-file deploy/.env.example -f deploy/docker-compose.yml config --quiet`

Expected: exit 0。

Run: `go test -tags=releaseintegration ./tests/releaseintegration -count=1`

Expected: PASS。

- [ ] **Step 6: 提交文档与最终本地状态**

```bash
git add deploy/RELEASE.md deploy/README.md deploy/PRODUCTION.md README.md
git commit -m "docs: 完成生产发布与回滚手册"
git status --short
```

Expected: working tree clean。

### Task 13: Pull Request、远程 CI 与候选产物验收

**Files:**
- No repository file changes expected。

**Interfaces:**
- Produces: reviewed Pull Request, five required green checks, merged `main`, successful `云令候选版本` run and three GHCR packages。

- [ ] **Step 1: 推送实现分支并创建 Pull Request**

After explicit user confirmation:

```bash
git push -u origin codex/versioned-production-release
gh pr create --base main --head codex/versioned-production-release --title "建立版本化候选与人工批准生产发布" --body-file "$TMPDIR/yunling-release-pr.md"
```

The PR body must summarize scope, security boundaries, tests, no database/agent upgrade, and external setup still pending.

- [ ] **Step 2: 等待并修复全部远程检查**

Run: `gh pr checks --watch <PR编号>`

Expected: 后端测试与构建、前端测试与构建、端到端测试、代理安装与打包、部署配置与镜像全部成功。

若有失败，在实现分支修复根因，重新运行受影响的完整本地检查，提交、推送并再次等待；不得跳过或重命名必需检查。

- [ ] **Step 3: 请求代码审查并复核发布边界**

Review the final diff for secret references, production writes before approval, unpinned Actions/images, migration commands, agent package inclusion, destructive Compose commands and missing rollback tests. Record review findings in the PR.

- [ ] **Step 4: 取得合并确认并合并**

After explicit user confirmation, squash-merge the Pull Request. Verify the `main` push CI succeeds before evaluating the candidate workflow.

- [ ] **Step 5: 验收第一个候选运行**

Verify the candidate run references the merged `main` SHA, publishes exactly three images, records three digests, has three valid attestations, uploads the 90-day manifest/bootstrap artifact, and contains no production SSH attempt. Download the manifest and independently validate it with `yunling-release manifest validate`.

### Task 14: GitHub production 环境与腾讯云受限入口

**Files:**
- External GitHub and Tencent Cloud configuration only; do not commit generated keys or Secrets。

**Interfaces:**
- Consumes: merged bootstrap artifact from Task 13。
- Produces: public GHCR packages, protected GitHub `production` environment, dedicated deploy key and installed Tencent release entrypoint。

- [ ] **Step 1: 设置 GHCR 可见性**

After explicit user confirmation, set only `yunling-services`、`yunling-web`、`yunling-ops` packages to public. Test anonymous digest pulls from a logged-out or credential-free environment. Do not expose any package containing configuration or Secrets.

- [ ] **Step 2: 创建并保护 `production` 环境**

After explicit user confirmation, configure one required reviewer, protected `main` only, and disable administrator bypass. Keep self-review allowed while there is only one production administrator. Add non-secret variables `PRODUCTION_HOST` and `PRODUCTION_SSH_USER=yunling-deploy`.

- [ ] **Step 3: 安全生成并分别写入部署凭据**

Generate a new Ed25519 key in a permission-0700 temporary directory with an empty passphrase because GitHub Actions is non-interactive. The user stores the private key directly in `PRODUCTION_SSH_PRIVATE_KEY` through GitHub's secret form and never pastes it into chat or command output. Capture the Tencent SSH host key through two independent trusted views, compare fingerprints, then store the exact known_hosts line in `PRODUCTION_SSH_KNOWN_HOSTS`.

The user separately copies the existing approved Feishu robot webhook and signing secret from their password manager into `PRODUCTION_FEISHU_WEBHOOK` and `PRODUCTION_FEISHU_SIGNING_SECRET`; neither value is read back.

- [ ] **Step 4: 校验 bootstrap Artifact 后安装受限入口**

After explicit Tencent production-write confirmation, verify `SHA256SUMS`, upload only the bootstrap archive and public key, extract into a new root-only temporary directory, and run:

```bash
sudo sh deploy/release/install.sh --public-key-file /root/yunling-release-bootstrap/deploy.pub
```

Verify ownership/modes, `visudo -cf /etc/sudoers.d/yunling-deploy`, forced authorized key options and `sshd -T`. Confirm normal SSH with the deploy key cannot obtain a shell and a malformed request cannot invoke Docker.

- [ ] **Step 5: 导入四容器与代理只读基线**

Run `sudo /usr/local/sbin/yunling-release bootstrap` from the existing production terminal. Verify four distinct current container image IDs are captured, the two agent packages match the exact lock, the read-only volume serves the same public manifest and hashes, and `bootstrap/current/override/audit` files are root-only. Stop and restore the existing Compose file if any hash or health check differs.

- [ ] **Step 6: 执行只读 preflight**

Run `sudo /usr/local/sbin/yunling-release preflight` and the production workflow's preflight path without approving a deployment. Expected: infrastructure healthy, disk at least 3 GiB, memory at least 512 MiB, all three compatibility hashes equal, no application container ID changes, no Git source pull and no image build.

### Task 15: 首次人工批准发布与回滚能力验收

**Files:**
- Modify after successful rollout: `deploy/PRODUCTION.md` with factual run ID, SHA, digests, timestamps and results。

**Interfaces:**
- Consumes: first successful candidate and fully configured production environment。
- Produces: first GHCR-backed production release, verified rollback state, Feishu notification and updated factual production record。

- [ ] **Step 1: 展示首次发布差异并取得最终确认**

Compare the candidate source SHA with the component baselines recorded in `deploy/PRODUCTION.md`: API/Web `c22dcaf`, Scheduler image `sha256:0ce903240d96ae673da423a786a867c7cef69ea221758f4517ea352aed9f0cb2`, Ops image `sha256:f92f70902194f95daa9f4583c36d3573d0018b9aca79b2e82e741324c2bee8ad`. Present code and image differences, candidate attestations, CI results and rollback target. Do not trigger production until the user explicitly confirms this exact candidate.

- [ ] **Step 2: 触发并人工批准生产工作流**

Run the manual `云令生产发布` workflow with `operation=deploy` and the exact candidate run ID. Wait for `production` review, ask the user to approve in GitHub, then monitor without bypassing protection rules.

- [ ] **Step 3: 验证发布结果和不变量**

Require workflow success, four application containers healthy, internal API/Web/Ops checks passing, public `https://aiwise.top/healthz` and `/api/health` returning success with valid TLS, current state pointing at exact GHCR digests, previous state pointing at bootstrap, and a successful Chinese Feishu card. Confirm PostgreSQL、Redis、MinIO、Caddy container IDs and all data volumes are unchanged. Download the public agent manifest and packages again and confirm the exact locked hashes.

- [ ] **Step 4: 验证人工回滚入口但不改变生产**

Trigger `operation=rollback,target_id=bootstrap` only through the workflow's no-secret preflight and stop before approving the `production` environment. Expected: input validation succeeds and deployment waits for review; reject/cancel it in GitHub. This proves the manual rollback entry exists without unnecessarily reverting a healthy production release.

- [ ] **Step 5: 更新事实生产记录并提交**

Write the actual candidate run ID, merged source SHA, three full image digests, first release time, health result, previous `bootstrap` target, agent lock hash and rollback-entry validation result to `deploy/PRODUCTION.md`. Create a documentation-only Pull Request and pass the existing five checks before merging with user confirmation.

- [ ] **Step 6: 宣布阶段完成**

Report the exact production version and evidence, remaining local bootstrap rollback availability, GHCR package visibility, GitHub environment protection, and the next independent phase: pre-release backup plus explicit database migrations. Do not claim database migration or agent upgrade automation exists.
