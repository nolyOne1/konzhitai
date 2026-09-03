# 云令 CI 质量门禁 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为云令建立只做验证、不接触生产环境的 GitHub Actions 完整质量门禁，并在五项检查首次通过后保护 `main` 分支。

**Architecture:** 使用一个最小权限的 `.github/workflows/ci.yml`，把后端、前端、端到端、代理发布链路、部署配置与应用镜像拆成五个彼此独立的 Ubuntu Job。用 Go 标准库静态契约测试约束工作流触发范围、权限、固定 Action SHA、稳定 Job 名称和禁止部署边界；真实工具链行为由各 Job 自身及首次 Pull Request 运行验证。

**Tech Stack:** GitHub Actions、Ubuntu 托管 Runner、Go 1.27、Node.js 24、npm、Vitest、Playwright、Bash、Docker Compose v2、Go 标准库测试。

**Spec:** `docs/superpowers/specs/2026-09-03-ci-quality-gates-design.md`

## Global Constraints

- CI 只验证代码，禁止部署、重启、迁移、连接腾讯云或京东云、调用飞书或访问 COS。
- 工作流只能使用 `permissions: contents: read`，不得引用任何 GitHub Secret。
- 所有 `uses:` 必须固定到 40 位提交 SHA，并保留主版本注释：
  - `actions/checkout@fbc6f3992d24b796d5a048ff273f7fcc4a7b6c09 # v5`
  - `actions/setup-go@924ae3a1cded613372ab5595356fb5720e22ba16 # v6`
  - `actions/setup-node@a0853c24544627f65ddf259abe73b1d18a591444 # v5`
  - `actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02 # v4`
- 五个 Job ID 固定为 `backend`、`web`、`e2e`、`agent`、`deployment`；显示名称固定为设计文档中的五个中文名称。
- 不使用 `needs` 串联五个 Job，不设置 `fail-fast`，让它们并行并完整报告失败。
- 不新增生产凭据、仓库 Secrets、自托管 Runner、部署步骤或可执行产物上传。
- 每个任务完成后先运行所列验证，再提交；失败时先修复根因，不跳过测试或放宽契约。

---

### Task 1: 建立工作流安全契约与五 Job 骨架

**Files:**
- Create: `.github/workflows/ci.yml`
- Create: `tests/integration/ci_workflow_test.go`
- Modify: `.gitattributes`

**Interfaces:**
- GitHub 工作流事件：`pull_request`、`push`、`workflow_dispatch`
- 稳定 Job ID：`backend`、`web`、`e2e`、`agent`、`deployment`
- 测试辅助函数：`readRepoText`、`ciJob`、`requireCIText`

- [ ] **Step 1: 写入会失败的安全契约测试**

创建 `tests/integration/ci_workflow_test.go`：

```go
package integration_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"yunling.local/platform/internal/testpostgres"
)

var (
	workflowActionPattern = regexp.MustCompile(`(?m)^\s*uses:\s*[^@\s]+@([^\s#]+)`)
	fullCommitSHA        = regexp.MustCompile(`^[0-9a-f]{40}$`)
	writePermission      = regexp.MustCompile(`(?m)^\s*[a-z-]+:\s*write\s*$`)
	nextWorkflowJob      = regexp.MustCompile(`(?m)^  [a-z][a-z0-9_-]*:\n`)
)

func readRepoText(t *testing.T, name string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(testpostgres.RepositoryRoot(t), filepath.FromSlash(name)))
	if err != nil {
		t.Fatalf("读取 %s：%v", name, err)
	}
	return strings.ReplaceAll(string(content), "\r\n", "\n")
}

func ciJob(t *testing.T, workflow, jobID string) string {
	t.Helper()
	marker := "  " + jobID + ":\n"
	start := strings.Index(workflow, marker)
	if start < 0 {
		t.Fatalf("CI 工作流缺少 Job %q", jobID)
	}
	tail := workflow[start+len(marker):]
	if next := nextWorkflowJob.FindStringIndex(tail); next != nil {
		tail = tail[:next[0]]
	}
	return tail
}

func requireCIText(t *testing.T, content string, required ...string) {
	t.Helper()
	for _, text := range required {
		if !strings.Contains(content, text) {
			t.Errorf("CI 契约缺少 %q", text)
		}
	}
}

func TestCIWorkflowUsesSafeTriggersPermissionsAndPins(t *testing.T) {
	workflow := readRepoText(t, ".github/workflows/ci.yml")
	requireCIText(t, workflow,
		"name: 云令 CI",
		"pull_request:\n    branches:\n      - main",
		"push:\n    branches:\n      - main",
		"workflow_dispatch:",
		"permissions:\n  contents: read",
		"group: ${{ github.workflow }}-${{ github.event.pull_request.number || github.ref }}",
		"cancel-in-progress: true",
	)
	for _, forbidden := range []string{
		"pull_request_target",
		"${{ secrets.",
		"id-token:",
		"packages:",
		"deployments:",
		"actions: write",
		"aiwise.top",
	} {
		if strings.Contains(workflow, forbidden) {
			t.Errorf("CI 工作流包含禁止内容 %q", forbidden)
		}
	}
	if writePermission.MatchString(workflow) {
		t.Error("CI 工作流不得申请写权限")
	}
	refs := workflowActionPattern.FindAllStringSubmatch(workflow, -1)
	if len(refs) == 0 {
		t.Fatal("CI 工作流至少应使用检出 Action")
	}
	for _, ref := range refs {
		if !fullCommitSHA.MatchString(ref[1]) {
			t.Errorf("Action 未固定到完整提交 SHA：%s", ref[1])
		}
	}
}

func TestCIWorkflowDefinesStableFiveJobs(t *testing.T) {
	workflow := readRepoText(t, ".github/workflows/ci.yml")
	checks := map[string]string{
		"backend":    "后端测试与构建",
		"web":        "前端测试与构建",
		"e2e":        "端到端测试",
		"agent":      "代理安装与打包",
		"deployment": "部署配置与镜像",
	}
	for jobID, displayName := range checks {
		job := ciJob(t, workflow, jobID)
		requireCIText(t, job, "name: "+displayName, "runs-on: ubuntu-latest")
		if strings.Contains(job, "needs:") {
			t.Errorf("Job %s 不得依赖其他质量门禁 Job", jobID)
		}
	}
}

func TestCIConfigFilesAreForcedToLF(t *testing.T) {
	attributes := readRepoText(t, ".gitattributes")
	requireCIText(t, attributes,
		"*.sh text eol=lf",
		"*.service text eol=lf",
		"Makefile text eol=lf",
		"Dockerfile* text eol=lf",
		"*.yml text eol=lf",
		"*.yaml text eol=lf",
	)
}
```

- [ ] **Step 2: 运行契约测试并确认红灯**

Run: `go test ./tests/integration -run 'TestCI' -count=1`

Expected: FAIL，明确报告 `.github/workflows/ci.yml` 不存在，并缺少 `*.yml`、`*.yaml` 的 LF 规则。

- [ ] **Step 3: 固定 YAML 换行并创建最小安全工作流**

在 `.gitattributes` 末尾加入：

```gitattributes
*.yml text eol=lf
*.yaml text eol=lf
```

创建 `.github/workflows/ci.yml`：

```yaml
name: 云令 CI

on:
  pull_request:
    branches:
      - main
  push:
    branches:
      - main
  workflow_dispatch:

permissions:
  contents: read

concurrency:
  group: ${{ github.workflow }}-${{ github.event.pull_request.number || github.ref }}
  cancel-in-progress: true

jobs:
  backend:
    name: 后端测试与构建
    runs-on: ubuntu-latest
    timeout-minutes: 30
    steps:
      - name: 检出代码
        uses: actions/checkout@fbc6f3992d24b796d5a048ff273f7fcc4a7b6c09 # v5

  web:
    name: 前端测试与构建
    runs-on: ubuntu-latest
    timeout-minutes: 15
    steps:
      - name: 检出代码
        uses: actions/checkout@fbc6f3992d24b796d5a048ff273f7fcc4a7b6c09 # v5

  e2e:
    name: 端到端测试
    runs-on: ubuntu-latest
    timeout-minutes: 20
    steps:
      - name: 检出代码
        uses: actions/checkout@fbc6f3992d24b796d5a048ff273f7fcc4a7b6c09 # v5

  agent:
    name: 代理安装与打包
    runs-on: ubuntu-latest
    timeout-minutes: 15
    steps:
      - name: 检出代码
        uses: actions/checkout@fbc6f3992d24b796d5a048ff273f7fcc4a7b6c09 # v5

  deployment:
    name: 部署配置与镜像
    runs-on: ubuntu-latest
    timeout-minutes: 30
    steps:
      - name: 检出代码
        uses: actions/checkout@fbc6f3992d24b796d5a048ff273f7fcc4a7b6c09 # v5
```

- [ ] **Step 4: 运行契约测试并确认绿灯**

Run: `go test ./tests/integration -run 'TestCI' -count=1`

Expected: PASS。

- [ ] **Step 5: 提交安全骨架**

```bash
git add .gitattributes .github/workflows/ci.yml tests/integration/ci_workflow_test.go
git commit -m "test: 约束 CI 安全边界"
```

---

### Task 2: 实现后端与前端质量门禁

**Files:**
- Modify: `tests/integration/ci_workflow_test.go`
- Modify: `.github/workflows/ci.yml`

**Interfaces:**
- 后端命令：`go test -race -p=1 ./... -count=1` 与五个 `cmd` 编译目标
- 前端命令：`npm ci`、`npm run test:web`、`npm run build:web`

- [ ] **Step 1: 追加后端与前端 Job 契约测试**

在 `tests/integration/ci_workflow_test.go` 末尾加入：

```go
func TestCIBackendAndWebJobsRunCompleteGates(t *testing.T) {
	workflow := readRepoText(t, ".github/workflows/ci.yml")
	backend := ciJob(t, workflow, "backend")
	requireCIText(t, backend,
		"uses: actions/setup-go@924ae3a1cded613372ab5595356fb5720e22ba16 # v6",
		"go-version-file: go.mod",
		"cache-dependency-path: go.sum",
		"go test -race -p=1 ./... -count=1",
		"./cmd/api",
		"./cmd/scheduler",
		"./cmd/ops",
		"./cmd/agent",
		"./cmd/bootstrap",
		"CGO_ENABLED: \"0\"",
	)

	web := ciJob(t, workflow, "web")
	requireCIText(t, web,
		"uses: actions/setup-node@a0853c24544627f65ddf259abe73b1d18a591444 # v5",
		"node-version: \"24\"",
		"cache: npm",
		"cache-dependency-path: package-lock.json",
		"npm ci",
		"npm run test:web",
		"npm run build:web",
	)
}
```

- [ ] **Step 2: 运行新增测试并确认红灯**

Run: `go test ./tests/integration -run 'TestCIBackendAndWebJobsRunCompleteGates' -count=1`

Expected: FAIL，报告缺少 Go/Node 工具链和验证命令。

- [ ] **Step 3: 用完整后端步骤替换 `backend.steps`**

```yaml
    steps:
      - name: 检出代码
        uses: actions/checkout@fbc6f3992d24b796d5a048ff273f7fcc4a7b6c09 # v5
      - name: 安装 Go
        uses: actions/setup-go@924ae3a1cded613372ab5595356fb5720e22ba16 # v6
        with:
          go-version-file: go.mod
          cache: true
          cache-dependency-path: go.sum
      - name: 运行 Go 测试与竞态检测
        run: go test -race -p=1 ./... -count=1
      - name: 编译五个服务程序
        env:
          CGO_ENABLED: "0"
        run: |
          mkdir -p "$RUNNER_TEMP/yunling-bin"
          go build -trimpath -o "$RUNNER_TEMP/yunling-bin/yunling-api" ./cmd/api
          go build -trimpath -o "$RUNNER_TEMP/yunling-bin/yunling-scheduler" ./cmd/scheduler
          go build -trimpath -o "$RUNNER_TEMP/yunling-bin/yunling-ops" ./cmd/ops
          go build -trimpath -o "$RUNNER_TEMP/yunling-bin/yunling-agent" ./cmd/agent
          go build -trimpath -o "$RUNNER_TEMP/yunling-bin/yunling-bootstrap" ./cmd/bootstrap
```

- [ ] **Step 4: 用完整前端步骤替换 `web.steps`**

```yaml
    steps:
      - name: 检出代码
        uses: actions/checkout@fbc6f3992d24b796d5a048ff273f7fcc4a7b6c09 # v5
      - name: 安装 Node.js
        uses: actions/setup-node@a0853c24544627f65ddf259abe73b1d18a591444 # v5
        with:
          node-version: "24"
          cache: npm
          cache-dependency-path: package-lock.json
      - name: 安装前端依赖
        run: npm ci
      - name: 运行前端单元与组件测试
        run: npm run test:web
      - name: 构建前端生产资源
        run: npm run build:web
```

- [ ] **Step 5: 验证契约及本地功能**

Run: `go test ./tests/integration -run 'TestCI' -count=1`

Expected: PASS。

Run: `npm ci`

Expected: PASS，`package-lock.json` 无变化。

Run: `npm run test:web`

Expected: PASS，现有 77 项测试通过。

Run: `npm run build:web`

Expected: PASS，TypeScript 和 Vite 构建成功。

- [ ] **Step 6: 提交后端与前端 Job**

```bash
git add .github/workflows/ci.yml tests/integration/ci_workflow_test.go
git commit -m "ci: 验证后端与前端构建"
```

---

### Task 3: 实现 Playwright 端到端检查与失败诊断

**Files:**
- Modify: `tests/integration/ci_workflow_test.go`
- Modify: `apps/web/playwright.config.ts`
- Modify: `.github/workflows/ci.yml`

**Interfaces:**
- E2E 命令：`npm run test:e2e`
- 失败产物：`apps/web/playwright-report`、`apps/web/test-results`
- 固定产物名：`playwright-failure-diagnostics`

- [ ] **Step 1: 追加 E2E 安全与诊断契约测试**

```go
func TestCIEndToEndJobRunsLocallyAndUploadsOnlyFailureDiagnostics(t *testing.T) {
	workflow := readRepoText(t, ".github/workflows/ci.yml")
	e2e := ciJob(t, workflow, "e2e")
	requireCIText(t, e2e,
		"uses: actions/setup-node@a0853c24544627f65ddf259abe73b1d18a591444 # v5",
		"node-version: \"24\"",
		"npm ci",
		"npx playwright install --with-deps chromium",
		"npm run test:e2e",
		"if: ${{ failure() || cancelled() }}",
		"uses: actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02 # v4",
		"name: playwright-failure-diagnostics",
		"apps/web/playwright-report",
		"apps/web/test-results",
		"retention-days: 7",
	)
	if strings.Contains(e2e, "YUNLING_E2E_BASE_URL") {
		t.Error("CI E2E 必须由 Playwright 启动本地 Vite，不得指定外部地址")
	}
	playwrightConfig := readRepoText(t, "apps/web/playwright.config.ts")
	requireCIText(t, playwrightConfig,
		"['github']",
		"['html', { outputFolder: 'playwright-report', open: 'never' }]",
		"screenshot: 'only-on-failure'",
		"trace: 'retain-on-failure'",
	)
}
```

- [ ] **Step 2: 运行新增测试并确认红灯**

Run: `go test ./tests/integration -run 'TestCIEndToEndJobRunsLocallyAndUploadsOnlyFailureDiagnostics' -count=1`

Expected: FAIL，报告 E2E 命令、HTML 报告器和失败上传配置缺失。

- [ ] **Step 3: 让 CI 同时生成 GitHub 注解和 HTML 报告**

把 `apps/web/playwright.config.ts` 中的 `reporter` 改为：

```ts
  reporter: process.env.CI
    ? [['github'], ['html', { outputFolder: 'playwright-report', open: 'never' }]]
    : 'list',
```

- [ ] **Step 4: 用完整 E2E 步骤替换 `e2e.steps`**

```yaml
    steps:
      - name: 检出代码
        uses: actions/checkout@fbc6f3992d24b796d5a048ff273f7fcc4a7b6c09 # v5
      - name: 安装 Node.js
        uses: actions/setup-node@a0853c24544627f65ddf259abe73b1d18a591444 # v5
        with:
          node-version: "24"
          cache: npm
          cache-dependency-path: package-lock.json
      - name: 安装前端依赖
        run: npm ci
      - name: 安装 Chromium 与系统依赖
        run: npx playwright install --with-deps chromium
      - name: 运行端到端测试
        run: npm run test:e2e
      - name: 上传失败诊断
        if: ${{ failure() || cancelled() }}
        uses: actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02 # v4
        with:
          name: playwright-failure-diagnostics
          path: |
            apps/web/playwright-report
            apps/web/test-results
          if-no-files-found: ignore
          retention-days: 7
```

- [ ] **Step 5: 验证契约、类型检查与现有 5 项浏览器流程**

Run: `go test ./tests/integration -run 'TestCI' -count=1`

Expected: PASS。

Run: `npm run build:web`

Expected: PASS，Playwright 配置通过 TypeScript 检查。

Run: `npx playwright install chromium`

Expected: PASS，安装锁文件所对应 Playwright 版本的 Chromium。

Run: `npm run test:e2e`

Expected: PASS，现有 5 项流程通过且访问 `127.0.0.1:4173`。

- [ ] **Step 6: 提交 E2E Job**

```bash
git add .github/workflows/ci.yml apps/web/playwright.config.ts tests/integration/ci_workflow_test.go
git commit -m "ci: 增加端到端失败诊断"
```

---

### Task 4: 实现代理安装、双架构编译与发布包校验

**Files:**
- Modify: `tests/integration/ci_workflow_test.go`
- Modify: `.github/workflows/ci.yml`

**Interfaces:**
- Shell 测试：`deploy/agent/install_test.sh`、`deploy/agent/package_test.sh`
- 发布包生成：`deploy/agent/package.sh 0.1.0 "$RUNNER_TEMP/yunling-agent-linux-amd64" "$RUNNER_TEMP/yunling-agent-linux-arm64" "$RUNNER_TEMP/yunling-agent-release"`
- 输出：两个 `.tar.gz` 和一个 `manifest.json`，仅保留在 Runner 临时目录

- [ ] **Step 1: 追加代理 Job 契约测试**

```go
func TestCIAgentJobTestsAndPackagesBothArchitectures(t *testing.T) {
	workflow := readRepoText(t, ".github/workflows/ci.yml")
	agent := ciJob(t, workflow, "agent")
	requireCIText(t, agent,
		"uses: actions/setup-go@924ae3a1cded613372ab5595356fb5720e22ba16 # v6",
		"go-version-file: go.mod",
		"bash -n deploy/agent/*.sh",
		"bash deploy/agent/install_test.sh",
		"sh deploy/agent/package_test.sh",
		"GOARCH=amd64",
		"GOARCH=arm64",
		"sh deploy/agent/package.sh",
		"manifest.json",
		"sha256sum",
		"byte_size",
	)
	if strings.Contains(agent, "actions/upload-artifact") {
		t.Error("代理发布包只能在 Runner 临时验证，不得上传")
	}
}
```

- [ ] **Step 2: 运行新增测试并确认红灯**

Run: `go test ./tests/integration -run 'TestCIAgentJobTestsAndPackagesBothArchitectures' -count=1`

Expected: FAIL，报告安装测试、双架构编译和清单校验缺失。

- [ ] **Step 3: 用完整代理步骤替换 `agent.steps`**

```yaml
    steps:
      - name: 检出代码
        uses: actions/checkout@fbc6f3992d24b796d5a048ff273f7fcc4a7b6c09 # v5
      - name: 安装 Go
        uses: actions/setup-go@924ae3a1cded613372ab5595356fb5720e22ba16 # v6
        with:
          go-version-file: go.mod
          cache: true
          cache-dependency-path: go.sum
      - name: 检查代理 Shell 语法
        run: bash -n deploy/agent/*.sh
      - name: 验证代理安装与失败恢复
        run: bash deploy/agent/install_test.sh
      - name: 验证代理发布包约束
        run: sh deploy/agent/package_test.sh
      - name: 真实构建并校验双架构发布包
        env:
          AGENT_VERSION: 0.1.0
        run: |
          release_dir="$RUNNER_TEMP/yunling-agent-release"
          mkdir -p "$release_dir"
          CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w -X main.agentVersion=${AGENT_VERSION}" -o "$RUNNER_TEMP/yunling-agent-linux-amd64" ./cmd/agent
          CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w -X main.agentVersion=${AGENT_VERSION}" -o "$RUNNER_TEMP/yunling-agent-linux-arm64" ./cmd/agent
          sh deploy/agent/package.sh "$AGENT_VERSION" "$RUNNER_TEMP/yunling-agent-linux-amd64" "$RUNNER_TEMP/yunling-agent-linux-arm64" "$release_dir"
          test "$(jq -r '.version' "$release_dir/manifest.json")" = "$AGENT_VERSION"
          test "$(jq '[.artifacts[] | select(.os == "linux" and (.arch == "amd64" or .arch == "arm64"))] | length' "$release_dir/manifest.json")" = "2"
          for arch in amd64 arm64; do
            file_name="$(jq -r --arg arch "$arch" '.artifacts[] | select(.arch == $arch) | .file_name' "$release_dir/manifest.json")"
            expected_sha="$(jq -r --arg arch "$arch" '.artifacts[] | select(.arch == $arch) | .sha256' "$release_dir/manifest.json")"
            byte_size="$(jq -r --arg arch "$arch" '.artifacts[] | select(.arch == $arch) | .byte_size' "$release_dir/manifest.json")"
            test -f "$release_dir/$file_name"
            test "$(sha256sum "$release_dir/$file_name" | cut -d ' ' -f 1)" = "$expected_sha"
            test "$(wc -c < "$release_dir/$file_name" | tr -d '[:space:]')" = "$byte_size"
          done
```

- [ ] **Step 4: 验证契约与代理脚本**

Run: `go test ./tests/integration -run 'TestCI' -count=1`

Expected: PASS。

Run: `bash -n deploy/agent/*.sh`

Expected: PASS，无语法输出。

Run: `bash deploy/agent/install_test.sh`

Expected: PASS，地址、令牌清理、注册恢复和已有身份修复断言全部通过。

Run: `sh deploy/agent/package_test.sh`

Expected: PASS，归档内容、大小、SHA-256 和非法输入断言全部通过。

- [ ] **Step 5: 提交代理 Job**

```bash
git add .github/workflows/ci.yml tests/integration/ci_workflow_test.go
git commit -m "ci: 验证代理安装与发布包"
```

---

### Task 5: 实现部署配置、LF 与应用镜像检查

**Files:**
- Modify: `tests/integration/ci_workflow_test.go`
- Modify: `.github/workflows/ci.yml`

**Interfaces:**
- 配置验证：`docker compose --env-file deploy/.env.example -f deploy/docker-compose.yml config --quiet`
- 镜像构建目标：`web api scheduler ops bootstrap`
- 明确排除：`minio` 构建、`up`、`run`、数据库迁移和数据卷创建

- [ ] **Step 1: 追加部署 Job 边界契约测试**

```go
func TestCIDeploymentJobValidatesWithoutStartingOrDeploying(t *testing.T) {
	workflow := readRepoText(t, ".github/workflows/ci.yml")
	deployment := ciJob(t, workflow, "deployment")
	requireCIText(t, deployment,
		"git ls-files -z -- '*.sh' '*.service' 'Makefile' 'Dockerfile*' '*.yml' '*.yaml'",
		"文件必须使用 LF 换行",
		"docker compose --env-file deploy/.env.example -f deploy/docker-compose.yml config --quiet",
		"docker compose --env-file deploy/.env.example -f deploy/docker-compose.yml build web api scheduler ops bootstrap",
	)
	for _, forbidden := range []string{
		"docker compose --env-file deploy/.env.example -f deploy/docker-compose.yml up",
		"docker compose --env-file deploy/.env.example -f deploy/docker-compose.yml run",
		"build web api scheduler ops bootstrap minio",
		"docker push",
		"ssh ",
	} {
		if strings.Contains(deployment, forbidden) {
			t.Errorf("部署验证 Job 包含越界命令 %q", forbidden)
		}
	}
}
```

- [ ] **Step 2: 运行新增测试并确认红灯**

Run: `go test ./tests/integration -run 'TestCIDeploymentJobValidatesWithoutStartingOrDeploying' -count=1`

Expected: FAIL，报告 LF、Compose 配置和应用镜像构建命令缺失。

- [ ] **Step 3: 用完整部署验证步骤替换 `deployment.steps`**

```yaml
    steps:
      - name: 检出代码
        uses: actions/checkout@fbc6f3992d24b796d5a048ff273f7fcc4a7b6c09 # v5
      - name: 验证 Linux 文件换行
        run: |
          failed=0
          while IFS= read -r -d '' file; do
            if LC_ALL=C grep -q $'\r' "$file"; then
              echo "::error file=$file::文件必须使用 LF 换行"
              failed=1
            fi
          done < <(git ls-files -z -- '*.sh' '*.service' 'Makefile' 'Dockerfile*' '*.yml' '*.yaml')
          test "$failed" -eq 0
      - name: 解析 Compose 配置
        run: docker compose --env-file deploy/.env.example -f deploy/docker-compose.yml config --quiet
      - name: 构建云令应用镜像
        run: docker compose --env-file deploy/.env.example -f deploy/docker-compose.yml build web api scheduler ops bootstrap
```

- [ ] **Step 4: 验证契约与 Compose 行为**

Run: `go test ./tests/integration -run 'TestCI' -count=1`

Expected: PASS。

Run: `docker compose --env-file deploy/.env.example -f deploy/docker-compose.yml config --quiet`

Expected: PASS，不创建容器或数据卷。

Run: `docker compose --env-file deploy/.env.example -f deploy/docker-compose.yml build web api scheduler ops bootstrap`

Expected: PASS，只构建云令应用镜像；命令不包含 `minio`，不启动任何服务。

- [ ] **Step 5: 提交部署验证 Job**

```bash
git add .github/workflows/ci.yml tests/integration/ci_workflow_test.go
git commit -m "ci: 校验部署配置与应用镜像"
```

---

### Task 6: 记录 CI 使用方式并完成本地总验收

**Files:**
- Create: `docs/CI.md`
- Modify: `README.md`

**Interfaces:**
- 文档必须列出五个稳定检查名称、失败诊断边界、无部署保证和 `main` 保护项。

- [ ] **Step 1: 创建 CI 运维说明**

创建 `docs/CI.md`，内容必须完整写明：

```markdown
# 云令持续集成

`.github/workflows/ci.yml` 在面向 `main` 的 Pull Request、`main` 推送和人工触发时运行，只做验证，不部署、不连接生产服务器，也不读取仓库 Secrets。

五项稳定检查为：

- 后端测试与构建
- 前端测试与构建
- 端到端测试
- 代理安装与打包
- 部署配置与镜像

端到端检查只访问 Runner 上由 Playwright 启动的 Vite 服务。失败或取消时上传固定名称 `playwright-failure-diagnostics`，内容仅限 Playwright HTML 报告、截图和 Trace，保留 7 天；成功时不上传。

`main` 必须启用 Pull Request、分支保持最新、以上五项必需检查、解决审查对话、禁止强制推送和禁止删除。当前审核批准数为 0；不得给普通成员或自动化配置绕过权限。

如果修改 Job 显示名称，必须先调整分支保护中的必需检查，再提交工作流改名，避免 Pull Request 永久等待旧名称。
```

- [ ] **Step 2: 从根 README 链接 CI 说明**

在 `README.md` 的项目简介之后、现有生产部署说明之前加入：

```markdown
## 持续集成

GitHub Actions 会并行运行五项完整质量门禁，但不会部署或连接生产环境。检查名称、失败诊断和 `main` 分支保护要求见 [docs/CI.md](docs/CI.md)。
```

- [ ] **Step 3: 执行静态、安全和应用总验收**

Run: `go test ./tests/integration -run 'TestCI' -count=1`

Expected: PASS。

Run: `go test -p=1 ./... -count=1`

Expected: PASS；当前 Windows 环境继续串行使用嵌入式 PostgreSQL 缓存，Linux CI 会额外启用 `-race`。

Run: `npm ci`

Expected: PASS，锁文件无变化。

Run: `npm run test:web`

Expected: PASS，现有 77 项前端测试通过。

Run: `npm run build:web`

Expected: PASS，TypeScript 和 Vite 生产构建完成。

Run: `npm run test:e2e`

Expected: PASS，现有 5 项 E2E 流程全部通过。

Run: `bash -n deploy/agent/*.sh`

Expected: PASS。

Run: `bash deploy/agent/install_test.sh`

Expected: PASS。

Run: `sh deploy/agent/package_test.sh`

Expected: PASS。

Run: `docker compose --env-file deploy/.env.example -f deploy/docker-compose.yml config --quiet`

Expected: PASS。

Run: `rg -n 'pull_request_target|secrets\.|aiwise\.top|docker push|docker compose .* up' .github/workflows/ci.yml`

Expected: exit 1 且无输出，表示没有危险内容。

Run: `git diff --check`

Expected: PASS，无空白错误。

- [ ] **Step 4: 检查计划规定的 Action 固定值**

Run: `rg -n 'uses:' .github/workflows/ci.yml`

Expected: 所有行都只出现 Global Constraints 中列出的四个 40 位 SHA，且行尾包含版本注释。

- [ ] **Step 5: 提交文档**

```bash
git add README.md docs/CI.md
git commit -m "docs: 说明 CI 与分支保护"
```

---

### Task 7: 推送分支并完成 GitHub 首次真实运行

**Files:**
- Inspect: `.github/workflows/ci.yml`
- Inspect: GitHub Pull Request checks

**Interfaces:**
- 远端分支：`codex/ci-quality-gates`
- 目标分支：`main`
- 仓库：`nolyOne1/konzhitai`

- [ ] **Step 1: 推送实现分支**

Run: `git status --short --branch`

Expected: 位于 `codex/ci-quality-gates`，工作区干净。

Run: `git push -u origin codex/ci-quality-gates`

Expected: 远端分支创建或更新成功；`main` 不发生变化。

- [ ] **Step 2: 创建面向 `main` 的 Pull Request**

优先使用已登录 GitHub 会话打开：

`https://github.com/nolyOne1/konzhitai/compare/main...codex/ci-quality-gates?expand=1`

标题固定为 `ci: 建立云令完整质量门禁`，正文说明“五项并行检查、只验证不部署、检查通过后启用 main 保护”。创建后记录 Pull Request 编号。

- [ ] **Step 3: 等待五项检查完成**

若本机已有已认证的 GitHub CLI，运行：

```bash
gh pr checks --repo nolyOne1/konzhitai --watch --interval 10
```

否则在已登录的 Pull Request“检查”页面等待。Expected: 以下五项全部 SUCCESS：

- `后端测试与构建`
- `前端测试与构建`
- `端到端测试`
- `代理安装与打包`
- `部署配置与镜像`

- [ ] **Step 4: 对真实 Runner 失败执行闭环修复**

仅在检查失败时运行：

```bash
RUN_ID="$(gh run list --repo nolyOne1/konzhitai --branch codex/ci-quality-gates --workflow ci.yml --limit 1 --json databaseId --jq '.[0].databaseId')"
test -n "$RUN_ID"
gh run view "$RUN_ID" --repo nolyOne1/konzhitai --log-failed
```

根据失败 Job 修复根因，先补充或收紧 `tests/integration/ci_workflow_test.go`，本地复现由红到绿，提交 `fix: 修复 CI 运行失败`，重新推送并再次等待全部五项检查。不得加入 `continue-on-error`、删除测试、缩小命令范围或引用生产配置。

- [ ] **Step 5: 验证失败诊断策略**

检查成功运行不应存在 `playwright-failure-diagnostics`。如果 E2E 曾真实失败或被取消，确认该次运行只有这个固定产物，保留期为 7 天，内容只有 `playwright-report` 和 `test-results`；不得为了制造失败而修改主业务测试或生产配置。

---

### Task 8: 启用并复核 `main` 分支保护

**Files:**
- Inspect: GitHub repository ruleset for `main`

**Interfaces:**
- Ruleset 名称：`main-quality-gate`
- 目标：默认分支 `main`
- 必需状态：五个稳定中文检查名称

- [ ] **Step 1: 在首次五项检查全部成功后创建规则**

在已登录 GitHub 中打开 `nolyOne1/konzhitai → 设置 → 规则 → 规则集 → 新建分支规则集`，按以下固定值保存：

- 名称：`main-quality-gate`
- 执行状态：`活动`
- 目标分支：默认分支 `main`
- 绕过列表：空
- Require a pull request before merging：开启
- Required approvals：`0`
- Require conversation resolution before merging：开启
- Require status checks to pass：开启
- Require branches to be up to date before merging：开启
- Required checks：加入五个首次运行产生的中文检查名称
- Block force pushes：开启
- Restrict deletions：开启

如仓库页面只提供经典分支保护，使用相同目标和约束，并开启“包括管理员/不允许绕过”，保证仓库所有者也不能日常直接推送。

- [ ] **Step 2: 只读复核规则内容**

若 GitHub CLI 有仓库管理读取权限：

```bash
RULESET_ID="$(gh api repos/nolyOne1/konzhitai/rulesets --jq '.[] | select(.name == "main-quality-gate") | .id')"
test -n "$RULESET_ID"
gh api "repos/nolyOne1/konzhitai/rulesets/$RULESET_ID"
```

Expected: `enforcement` 为 `active`，目标包含 `main`，无 bypass actor，Pull Request 规则批准数为 0，要求解决对话，五项 status check 全部存在并要求最新分支，删除和强推受到限制。

若接口权限不足，在规则集页面逐项复核并保存截图；不要临时创建高权限访问密钥粘贴到终端或仓库。

- [ ] **Step 3: 验证当前 Pull Request 被规则正确管理**

Expected: 五项检查通过且分支为最新时允许合并；任一检查重新运行、失败或分支落后时合并按钮受阻。不通过向 `main` 写入测试提交来验证直接推送限制。

---

### Task 9: 经用户批准后合并并验证 `main`

**Files:**
- Inspect: GitHub Pull Request merge state
- Inspect: GitHub Actions run on `main`

**Interfaces:**
- 合并方式：普通 Merge Pull Request
- 合并后事件：`push` to `main`

- [ ] **Step 1: 向用户报告五项检查与保护规则证据，等待明确合并授权**

报告 Pull Request 链接、五项状态、规则集状态和“无部署步骤”的复核结果。未得到本阶段明确授权前停止，不合并 Pull Request。

- [ ] **Step 2: 获得授权后合并 Pull Request**

在 GitHub 页面选择普通合并并保留 `codex/ci-quality-gates` 分支，便于短期审计和回退。Expected: 合并由保护规则放行，不能绕过检查。

- [ ] **Step 3: 等待 `main` 的合并后复验**

若 GitHub CLI 可用：

```bash
MAIN_RUN_ID="$(gh run list --repo nolyOne1/konzhitai --branch main --workflow ci.yml --limit 1 --json databaseId --jq '.[0].databaseId')"
test -n "$MAIN_RUN_ID"
gh run watch "$MAIN_RUN_ID" --repo nolyOne1/konzhitai --exit-status
```

否则在仓库“操作”页面打开由该合并提交触发的 `云令 CI`。Expected: 同一五项检查全部 SUCCESS，且没有部署环境、Release、Package 或生产服务器变更。

- [ ] **Step 4: 完成最终审计**

Run: `git fetch origin main`

Expected: `origin/main` 包含 CI Pull Request 的合并提交。

最终报告必须包含：Pull Request、合并提交、五项检查结果、`main-quality-gate` 已启用、成功运行没有 Playwright 产物、生产环境未被访问或修改。
