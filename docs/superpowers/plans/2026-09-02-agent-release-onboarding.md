# 云令代理发布与一键接入 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为云令发布经过 SHA-256 校验的 Linux x86_64/ARM64 代理安装包，并把服务器接入向导升级为不泄露注册令牌的一条命令安装。

**Architecture:** 服务镜像构建阶段交叉编译两种代理并生成只读发布目录；独立 `internal/agentrelease` 包在 API 启动时校验清单和文件，再通过公开、不可变的 `/api/releases/agent/*` 路由提供下载。Web 先加载发布清单，再允许签发一次性令牌，并生成负责选架构、下载、验哈希、解包和调用安全安装器的 Bash 命令。

**Tech Stack:** Go 1.27、Go `net/http`、Linux ELF、POSIX Shell/Bash、Docker BuildKit、React 19、TypeScript 7、Vitest、Testing Library、Docker Compose、Caddy。

**Spec:** `docs/superpowers/specs/2026-09-02-agent-release-onboarding-design.md`

## Global Constraints

- 首期只支持 `linux/amd64` 与 `linux/arm64`；用户界面分别显示 Linux x86_64 与 Linux ARM64。
- 代理默认版本为 `0.1.0`，构建时通过链接参数覆盖；二进制自报版本、安装包版本和发布清单版本必须一致。
- 清单接口为 `GET /api/releases/agent/latest`；安装包接口为 `GET /api/releases/agent/{version}/{sha256}/{fileName}`。
- 清单响应不得长期缓存；带内容哈希的安装包响应必须使用一年不可变缓存和 ETag。
- 公开安装包不得包含令牌、长期凭据、部署秘密、生产地址或服务器身份。
- 复制命令不得包含注册令牌；安装程序只能从交互式终端隐藏读取令牌，不能从参数、环境变量或普通标准输入重定向读取。
- 下载、解包和 SHA-256 校验必须在提权前完成；未经校验的内容不得以 root 执行。
- 安装器不执行 `apt`、`yum`、`dnf`、`apk` 或其他包管理操作。
- 缺少 Bash、systemd 240+、polkit、`curl`/`wget`、`tar`、`sha256sum`、root/`sudo` 或交互终端时，安全停止并显示中文修复说明。
- 发布清单完整可用前不得创建一次性注册令牌；下载或校验失败不消费令牌，注册成功才消费。
- 不增加数据库迁移，不实现已接入代理自动升级，不重装现有京东云代理。
- 先写失败测试，再写最小实现；每个任务通过专项测试后独立提交。

## File Structure

- `cmd/agent/main.go`：保留代理默认版本、响应 `version` 命令，并把同一版本用于心跳上报。
- `deploy/agent/package.sh`：把两个代理与固定安装资产打包，计算哈希和字节数，生成内部 `manifest.json`。
- `deploy/agent/package_test.sh`：验证文件集合、清单字段、哈希和错误退出。
- `deploy/agent/install.sh`：负责预检、安全落盘、隐藏读取令牌、注册和失败清理。
- `deploy/agent/install_test.sh`：验证安装器参数、安全输入边界、已有凭据保护和清理函数。
- `deploy/Dockerfile.services`：构建原生服务程序、两种代理、发布目录，并复制到只读运行镜像。
- `internal/agentrelease/catalog.go`：解析并验证内部清单、文件类型、大小、哈希和支持架构。
- `internal/agentrelease/http.go`：输出公共清单、不可变安装包、缓存头和统一不可用响应。
- `cmd/api/main.go`：在数据库初始化之外加载发布目录并挂载公开路由。
- `apps/web/src/api/client.ts`：声明发布清单类型并请求当前版本。
- `apps/web/src/features/servers/agentInstallCommand.ts`：验证同源清单并生成安全安装命令。
- `apps/web/src/features/servers/ServerEnrollmentDialog.tsx`：管理清单加载/失败/就绪/签发状态。
- `apps/web/src/app/styles.css`：补充版本状态、错误重试和移动端布局。
- `deploy/docker-compose.yml`：为 API 明确配置只读发布目录。
- `deploy/README.md`、`deploy/PRODUCTION.md`：记录操作方法与生产验收证据。

---

### Task 1: 统一代理构建版本与自检命令

**Files:**
- Modify: `cmd/agent/main.go:3-25`
- Modify: `cmd/agent/main_test.go`

**Interfaces:**
- Produces: 可由 `-ldflags "-X main.agentVersion=${AGENT_VERSION}"` 覆盖的 `var agentVersion`。
- Produces: `writeVersionCommand(args []string, output io.Writer) bool`。
- Preserves: 心跳继续使用同一个 `agentVersion` 值。

- [ ] **Step 1: 写版本命令失败测试**

```go
func TestWriteVersionCommandPrintsBuildVersion(t *testing.T) {
	original := agentVersion
	agentVersion = "9.8.7-test"
	t.Cleanup(func() { agentVersion = original })
	var output bytes.Buffer
	if !writeVersionCommand([]string{"yunling-agent", "version"}, &output) {
		t.Fatal("version 子命令必须被处理")
	}
	if output.String() != "9.8.7-test\n" { t.Fatalf("版本输出：%q", output.String()) }
}

func TestWriteVersionCommandIgnoresNormalStart(t *testing.T) {
	if writeVersionCommand([]string{"yunling-agent"}, io.Discard) {
		t.Fatal("普通启动不得被截断")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./cmd/agent -run 'VersionCommand' -count=1`

Expected: FAIL，提示函数未定义或常量不可赋值。

- [ ] **Step 3: 实现可注入版本**

```go
var agentVersion = "0.1.0"

func writeVersionCommand(args []string, output io.Writer) bool {
	if len(args) != 2 || args[1] != "version" { return false }
	fmt.Fprintln(output, agentVersion)
	return true
}

func main() {
	if writeVersionCommand(os.Args, os.Stdout) { return }
	// 后面保留现有 run-spec、注册、心跳和执行顺序。
}
```

只新增 `fmt` 和 `io` 导入，不复制第二个版本变量。

- [ ] **Step 4: 运行并提交**

Run: `go test ./cmd/agent -count=1`

Expected: PASS。

```bash
git add cmd/agent/main.go cmd/agent/main_test.go
git commit -m "feat: 支持注入代理构建版本"
```

### Task 2: 构建双架构不可变代理包

**Files:**
- Create: `deploy/agent/package.sh`
- Create: `deploy/agent/package_test.sh`
- Modify: `deploy/Dockerfile.services`

**Interfaces:**
- Consumes: Task 1 的 `main.agentVersion`。
- Produces: `package.sh VERSION AMD64_BINARY ARM64_BINARY OUTPUT_DIR`。
- Produces: `manifest.json` 与两个 `yunling-agent-{version}-linux-{arch}.tar.gz`。
- Produces: 镜像目录 `/opt/yunling/releases/agent`。

- [ ] **Step 1: 写打包器失败测试**

创建 `deploy/agent/package_test.sh`：

```sh
#!/bin/sh
set -eu
root_dir=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
test_dir=$(mktemp -d)
trap 'rm -rf "$test_dir"' EXIT HUP INT TERM
printf '#!/bin/sh\necho amd64\n' >"$test_dir/amd64"
printf '#!/bin/sh\necho arm64\n' >"$test_dir/arm64"
chmod 0755 "$test_dir/amd64" "$test_dir/arm64"
sh "$root_dir/deploy/agent/package.sh" 0.1.0 "$test_dir/amd64" "$test_dir/arm64" "$test_dir/out"
test "$(find "$test_dir/out" -name '*.tar.gz' -type f | wc -l | tr -d ' ')" = 2
for archive in "$test_dir/out"/*.tar.gz; do
  test "$(tar -tzf "$archive" | sort | tr '\n' ' ')" = "50-yunling-agent.rules install.sh yunling-agent yunling-agent.service yunling-run@.service "
done
grep -q '"arch":"amd64"' "$test_dir/out/manifest.json"
grep -q '"arch":"arm64"' "$test_dir/out/manifest.json"
```

同一测试再断言版本 `bad/version`、缺失二进制和不可执行二进制均返回非零。

- [ ] **Step 2: 运行测试确认失败**

Run: `docker run --rm -v "$PWD:/src" -w /src alpine:3.24 sh deploy/agent/package_test.sh`

Expected: FAIL，提示 `package.sh` 不存在。

- [ ] **Step 3: 实现固定集合打包器**

`package.sh` 使用 POSIX Shell；版本只接受 `[0-9A-Za-z][0-9A-Za-z._-]{0,63}`，两个二进制必须是普通可执行文件。对 amd64、arm64 分别执行：

```sh
stage=$(mktemp -d)
install -m 0755 "$binary" "$stage/yunling-agent"
install -m 0755 deploy/agent/install.sh "$stage/install.sh"
install -m 0644 deploy/agent/yunling-agent.service deploy/agent/yunling-run@.service deploy/agent/50-yunling-agent.rules "$stage/"
file_name="yunling-agent-${version}-linux-${arch}.tar.gz"
tar -czf "$output_dir/$file_name" -C "$stage" 50-yunling-agent.rules install.sh yunling-agent yunling-agent.service yunling-run@.service
sha256=$(sha256sum "$output_dir/$file_name" | cut -d ' ' -f 1)
byte_size=$(wc -c <"$output_dir/$file_name" | tr -d ' ')
rm -rf "$stage"
```

用 `printf` 写紧凑 JSON；顶层只有 `version`、`artifacts`，记录只有 `os`、`arch`、`file_name`、`byte_size`、`sha256`，顺序固定 amd64、arm64，不写路径或下载 URL。

- [ ] **Step 4: 运行打包测试**

Run: `docker run --rm -v "$PWD:/src" -w /src alpine:3.24 sh deploy/agent/package_test.sh`

Expected: PASS。

- [ ] **Step 5: 修改 Docker 构建**

在 builder 中增加 `ARG AGENT_VERSION=0.1.0`，原生代理和两个交叉编译代理都使用同一链接参数：

```dockerfile
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -X main.agentVersion=${AGENT_VERSION}" -o /out/yunling-agent ./cmd/agent && \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w -X main.agentVersion=${AGENT_VERSION}" -o /out/yunling-agent-linux-amd64 ./cmd/agent && \
    CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w -X main.agentVersion=${AGENT_VERSION}" -o /out/yunling-agent-linux-arm64 ./cmd/agent
RUN sh deploy/agent/package.sh "${AGENT_VERSION}" /out/yunling-agent-linux-amd64 /out/yunling-agent-linux-arm64 /out/agent-releases
```

运行镜像增加：

```dockerfile
COPY --from=builder --chown=10001:10001 /out/agent-releases /opt/yunling/releases/agent
```

保留现有 `/usr/local/bin/yunling-agent`，避免行为回退。

- [ ] **Step 6: 构建检查并提交**

Run: `docker build --build-arg AGENT_VERSION=0.1.0 -f deploy/Dockerfile.services -t yunling-services:agent-release-test .`

Run: `docker run --rm --entrypoint sh yunling-services:agent-release-test -c 'cat /opt/yunling/releases/agent/manifest.json && ls -l /opt/yunling/releases/agent'`

Expected: 清单有两个架构，目录只有两个归档和 `manifest.json`。

```bash
git add deploy/agent/package.sh deploy/agent/package_test.sh deploy/Dockerfile.services
git commit -m "build: 发布双架构云令代理包"
```

### Task 3: 加载并验证代理发布目录

**Files:**
- Create: `internal/agentrelease/catalog.go`
- Create: `internal/agentrelease/catalog_test.go`

**Interfaces:**
- Consumes: Task 2 的内部清单和归档。
- Produces: `Load(root string) (*Catalog, error)`、`(*Catalog).Manifest() Manifest`。
- Produces: `(*Catalog).Open(version, sha256, fileName string) (*os.File, Artifact, error)`。
- Produces: `ErrArtifactNotFound`。

- [ ] **Step 1: 写清单加载失败测试**

用 `t.TempDir()` 创建两个真实文件、清单和真实摘要。成功测试：

```go
catalog, err := Load(root)
if err != nil { t.Fatal(err) }
manifest := catalog.Manifest()
if manifest.Version != "0.1.0" || len(manifest.Artifacts) != 2 { t.Fatalf("清单：%+v", manifest) }
if manifest.Artifacts[0].Arch != "amd64" || manifest.Artifacts[1].Arch != "arm64" { t.Fatalf("顺序：%+v", manifest.Artifacts) }
for _, artifact := range manifest.Artifacts {
	want := "/api/releases/agent/0.1.0/" + artifact.SHA256 + "/" + artifact.FileName
	if artifact.DownloadURL != want { t.Fatalf("下载地址：%s", artifact.DownloadURL) }
}
```

表驱动拒绝：缺文件、符号链接、错误大小、错误 SHA-256、未知架构、重复 amd64、缺 arm64、非法版本、文件名含 `/`、未知 JSON 字段、尾随 JSON。

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/agentrelease -run Catalog -count=1`

Expected: FAIL，提示包或 `Load` 不存在。

- [ ] **Step 3: 定义类型并实现验证**

```go
var ErrArtifactNotFound = errors.New("代理安装包不存在")

type Artifact struct {
	OS string `json:"os"`; Arch string `json:"arch"`; FileName string `json:"file_name"`
	ByteSize int64 `json:"byte_size"`; SHA256 string `json:"sha256"`; DownloadURL string `json:"download_url"`
}
type Manifest struct { Version string `json:"version"`; Artifacts []Artifact `json:"artifacts"` }
type Catalog struct {
	manifest Manifest
	files map[string]string
	artifactByKey map[string]Artifact
}
```

内部 decoder 类型不含 `download_url`。使用 `DisallowUnknownFields()` 并要求解码后 EOF。版本、文件名、摘要用正则限制；支持集合固定为 `linux/amd64` 和 `linux/arm64`。每项使用 `os.Lstat` 拒绝非普通文件/符号链接，核对大小并流式计算摘要。

`Open` 只使用启动时建立的复合键：

```go
func artifactKey(version, digest, fileName string) string { return version + "\x00" + digest + "\x00" + fileName }
func (c *Catalog) Open(version, digest, fileName string) (*os.File, Artifact, error) {
	key := artifactKey(version, digest, fileName)
	path, ok := c.files[key]
	if !ok { return nil, Artifact{}, ErrArtifactNotFound }
	file, err := os.Open(path)
	if err != nil { return nil, Artifact{}, fmt.Errorf("打开代理安装包：%w", err) }
	return file, c.artifactByKey[key], nil
}
```

`Manifest()` 返回切片副本，调用者不能修改内部状态。

- [ ] **Step 4: 运行并提交**

Run: `go test ./internal/agentrelease -run Catalog -count=1`

Expected: PASS。

```bash
git add internal/agentrelease/catalog.go internal/agentrelease/catalog_test.go
git commit -m "feat: 校验代理发布目录"
```

### Task 4: 提供公开清单和不可变下载接口

**Files:**
- Create: `internal/agentrelease/http.go`
- Create: `internal/agentrelease/http_test.go`
- Modify: `cmd/api/main.go:1-45,195-230`
- Modify: `cmd/api/main_test.go`
- Modify: `deploy/docker-compose.yml:4-18`

**Interfaces:**
- Consumes: Task 3 的 Catalog。
- Produces: `Handler(catalog *Catalog, options ...HandlerOption) http.Handler`、`UnavailableHandler() http.Handler`。
- Produces: `WithLimits(maxRequestsPerWindow, maxConcurrent int, window time.Duration, now func() time.Time) HandlerOption`，仅测试和受控装配使用；生产省略 option 使用固定默认值。
- Produces: `loadAgentReleaseHandler(root string) http.Handler`。
- Produces: `newHTTPServer(address string, handler http.Handler) *http.Server`。
- Produces: 默认每分钟 240 个公开请求、最多 16 个并发下载的进程内保护边界。
- Produces: `YUNLING_AGENT_RELEASE_DIR`，默认 `/opt/yunling/releases/agent`。

- [ ] **Step 1: 写 HTTP 失败测试**

测试清单 200、JSON、`Cache-Control: no-cache, no-store, must-revalidate`、`nosniff`。测试归档 body 精确、`application/gzip`、Content-Length、Content-Disposition、完整摘要 ETag、`public, max-age=31536000, immutable`；携带相同 If-None-Match 得到 304 和空 body。

拒绝测试：POST 清单、错误版本/摘要/文件名、编码后的 `../`、清单旁的未列出文件；Unavailable handler 必须返回 503 和固定 `代理发布暂不可用`。通过测试 option 把窗口限制设为每分钟 2 次，第三次请求必须得到 429、`Retry-After: 5`；把并发槽设为 1 时，第二个同时下载必须得到相同的 429。

```go
request := httptest.NewRequest(http.MethodGet, "/api/releases/agent/latest", nil)
response := httptest.NewRecorder()
Handler(catalog).ServeHTTP(response, request)
if response.Code != http.StatusOK { t.Fatalf("状态码：%d", response.Code) }
if response.Header().Get("Cache-Control") != "no-cache, no-store, must-revalidate" { t.Fatal("缓存头错误") }
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/agentrelease -run 'HTTP|Handler|ETag' -count=1`

Expected: FAIL，提示 Handler 不存在。

- [ ] **Step 3: 实现路由和响应头**

```go
func Handler(catalog *Catalog, options ...HandlerOption) http.Handler {
	router := http.NewServeMux()
	router.HandleFunc("GET /api/releases/agent/latest", manifestHandler(catalog))
	router.HandleFunc("GET /api/releases/agent/{version}/{sha256}/{fileName}", artifactHandler(catalog))
	return withReleaseLimits(router, options...)
}
```

清单编码 Manifest 副本；下载 handler 只把 `ErrArtifactNotFound` 映射 404，其他读取错误用固定中文 500。ETag 是带双引号完整 SHA-256；匹配 If-None-Match 时不打开/发送 body。`withReleaseLimits` 使用互斥锁维护一分钟固定窗口计数，并用容量 16 的 channel 限制并发归档响应；测试 option 注入窗口、上限和 clock，不按客户端可伪造的代理头识别来源。窗口状态只保存计数和重置时间，不保存 IP 或 URL。

- [ ] **Step 4: 运行包测试**

Run: `go test ./internal/agentrelease -count=1`

Expected: PASS。

- [ ] **Step 5: 写 API 装配失败测试**

```go
func TestLoadAgentReleaseHandlerFailsClosed(t *testing.T) {
	handler := loadAgentReleaseHandler(t.TempDir())
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/releases/agent/latest", nil))
	if response.Code != http.StatusServiceUnavailable { t.Fatalf("状态码：%d", response.Code) }
}
```

- [ ] **Step 6: 在数据库初始化之外装配**

```go
releaseRoot := strings.TrimSpace(os.Getenv("YUNLING_AGENT_RELEASE_DIR"))
if releaseRoot == "" { releaseRoot = "/opt/yunling/releases/agent" }
router.Handle("/api/releases/agent/", loadAgentReleaseHandler(releaseRoot))

func loadAgentReleaseHandler(root string) http.Handler {
	catalog, err := agentrelease.Load(root)
	if err != nil { log.Print("代理发布目录校验失败，发布接口将返回暂不可用"); return agentrelease.UnavailableHandler() }
	manifest := catalog.Manifest()
	log.Printf("代理发布已就绪：版本 %s，架构 %d", manifest.Version, len(manifest.Artifacts))
	return agentrelease.Handler(catalog)
}
```

不要加认证中间件。把 `http.ListenAndServe` 改为 `newHTTPServer(address, router).ListenAndServe()`；`newHTTPServer` 固定 `ReadHeaderTimeout=5s`、`ReadTimeout=30s`、`WriteTimeout=5m`、`IdleTimeout=60s`，并在 `cmd/api/main_test.go` 精确断言这些值。Compose 的 `x-api-environment` 增加 `YUNLING_AGENT_RELEASE_DIR: /opt/yunling/releases/agent`，不增加 volume 或端口。

- [ ] **Step 7: 运行并提交**

Run: `go test ./internal/agentrelease ./cmd/api -count=1`

Run: `go build ./cmd/api ./cmd/agent`

Expected: 全部通过。

```bash
git add internal/agentrelease cmd/api/main.go cmd/api/main_test.go deploy/docker-compose.yml
git commit -m "feat: 提供代理安装包发布接口"
```

### Task 5: 收紧安装器令牌与失败清理

**Files:**
- Modify: `deploy/agent/install.sh`
- Create: `deploy/agent/install_test.sh`

**Interfaces:**
- Consumes: `install.sh --control-url https://控制平面域名`。
- Produces: `validate_control_url`、`check_preflight`、`strip_bootstrap_lines`、`cleanup_install`、`main`。
- Removes: 从环境变量或位置参数读取令牌的旧接口。

- [ ] **Step 1: 写安全边界失败测试**

```bash
#!/usr/bin/env bash
set -euo pipefail
root_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
export YUNLING_INSTALL_TESTING=1
source "${root_dir}/deploy/agent/install.sh"
validate_control_url 'https://aiwise.top'
validate_control_url 'https://control.example:8443'
if validate_control_url 'http://aiwise.top'; then echo '必须拒绝 HTTP' >&2; exit 1; fi
if validate_control_url 'https://aiwise.top/path'; then echo '必须只接受 origin' >&2; exit 1; fi
test "$(strip_bootstrap_lines $'A=1\nYUNLING_CONTROL_URL=x\nYUNLING_ENROLLMENT_TOKEN=y\nB=2\n')" = $'A=1\nB=2'
```

静态断言脚本不含包管理命令，不再要求从环境变量提供令牌，并存在 `/dev/tty`、`read -r -s`、`trap cleanup_install EXIT` 和已有凭据停止提示。

- [ ] **Step 2: 运行测试确认失败**

Run: `docker run --rm -v "$PWD:/src" -w /src bash:5.3 bash deploy/agent/install_test.sh`

Expected: FAIL，提示函数未定义。

- [ ] **Step 3: 拆分并实现预检函数**

```bash
validate_control_url() { [[ "$1" =~ ^https://[A-Za-z0-9][A-Za-z0-9.-]*(:[0-9]{1,5})?$ ]]; }

strip_bootstrap_lines() {
  while IFS= read -r line; do
    case "$line" in YUNLING_CONTROL_URL=*|YUNLING_ENROLLMENT_TOKEN=*) continue;; esac
    printf '%s\n' "$line"
  done <<<"$1"
}

if [[ "${YUNLING_INSTALL_TESTING:-0}" != 1 ]]; then main "$@"; fi
```

`parse_args` 只接受 `--control-url VALUE`。`check_preflight` 明确检查 root、systemd 240+、polkit 目录、`getent`、`groupadd`、`useradd`、`usermod`、`install`、`systemctl`、`mktemp` 和可读写 `/dev/tty`，只报告问题，不调用包管理器。

- [ ] **Step 4: 实现隐藏输入和清理**

在落盘前执行：

```bash
unset YUNLING_ENROLLMENT_TOKEN YUNLING_CONTROL_URL
if [[ -f "$credentials_path" || -f "$legacy_credentials_path" ]]; then
  echo '该服务器已存在云令代理身份，请先在控制台确认后再处理。' >&2
  exit 2
fi
IFS= read -r -s -p '请输入一次性注册令牌：' enrollment_token </dev/tty
printf '\n' >/dev/tty
[[ -n "$enrollment_token" ]] || { echo '注册令牌不能为空。' >&2; exit 1; }
```

`cleanup_install` 保存 `$?`、移除 EXIT trap并清空变量；注册未完成时停止刚启动的服务并原子删除 `agent.env` 中两条引导秘密，再以原退出码退出。成功取得凭据后先删除秘密，再设置 `registration_complete=1` 并重启。小写 token 不 export，只在 root-only env 文件短暂写一行；日志不得拼接 token。

- [ ] **Step 5: 运行并提交**

Run: `docker run --rm -v "$PWD:/src" -w /src bash:5.3 bash -n deploy/agent/install.sh`

Run: `docker run --rm -v "$PWD:/src" -w /src bash:5.3 bash deploy/agent/install_test.sh`

Run: `docker run --rm -v "$PWD:/src" -w /src alpine:3.24 sh deploy/agent/package_test.sh`

Expected: 全部 PASS；失败模拟返回非零且输出不含测试令牌。

```bash
git add deploy/agent/install.sh deploy/agent/install_test.sh
git commit -m "feat: 加固代理交互式安装流程"
```

### Task 6: 获取清单并生成安全安装命令

**Files:**
- Modify: `apps/web/src/api/client.ts:49-60,331-365`
- Modify: `apps/web/src/api/client.test.ts`
- Create: `apps/web/src/features/servers/agentInstallCommand.ts`
- Create: `apps/web/src/features/servers/agentInstallCommand.test.ts`

**Interfaces:**
- Produces: `AgentReleaseArtifact`、`AgentReleaseManifest`。
- Produces: `getLatestAgentRelease(): Promise<AgentReleaseManifest>`。
- Produces: `buildAgentInstallCommand(controlUrl string, release AgentReleaseManifest): string`。
- Produces: 固定异常 `代理发布清单不完整，请重新加载。`。

- [ ] **Step 1: 写客户端映射失败测试**

```ts
vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true, status: 200, json: async () => ({
  version: '0.1.0', artifacts: [{ os: 'linux', arch: 'amd64', file_name: 'agent-amd64.tar.gz',
    byte_size: 42, sha256: 'a'.repeat(64), download_url: `/api/releases/agent/0.1.0/${'a'.repeat(64)}/agent-amd64.tar.gz` }],
}) }))
expect((await getLatestAgentRelease()).artifacts[0]).toMatchObject({ fileName: 'agent-amd64.tar.gz', byteSize: 42 })
```

- [ ] **Step 2: 运行确认失败**

Run: `npm --workspace apps/web test -- --run src/api/client.test.ts`

Expected: FAIL，提示函数不存在。

- [ ] **Step 3: 增加类型和显式映射**

公共类型使用 camelCase；`getLatestAgentRelease` 请求 `/api/releases/agent/latest`，把每条 snake_case 字段逐个映射，不直接把响应类型断言为公共类型。

- [ ] **Step 4: 写命令生成失败测试**

```ts
const command = buildAgentInstallCommand('https://aiwise.top', release)
expect(command).toContain('Linux:x86_64')
expect(command).toContain('Linux:aarch64|Linux:arm64')
expect(command).toContain('mktemp -d')
expect(command).toContain('sha256sum')
expect(command).toContain('curl')
expect(command).toContain('wget')
expect(command).toContain('sudo -- bash "$extract_dir/install.sh" --control-url "$control_url"')
expect(command).not.toContain('YUNLING_ENROLLMENT_TOKEN')
expect(command).not.toContain('/tmp/install.sh')
```

表驱动拒绝缺 arm64、未知/重复架构、非 Linux、坏摘要、非正字节数、文件名含 `/`、绝对/协议相对/跨源 URL，以及 URL 版本/摘要/文件名和字段不一致。

- [ ] **Step 5: 运行确认失败**

Run: `npm --workspace apps/web test -- --run src/features/servers/agentInstallCommand.test.ts`

Expected: FAIL，提示模块不存在。

- [ ] **Step 6: 实现验证和命令骨架**

先要求两个固定架构各一条，`downloadUrl` 以单个 `/api/releases/agent/` 开头；`new URL(downloadUrl, controlUrl).origin` 必须等于控制平面 origin，路径中的版本、摘要、文件名必须精确对应字段。校验后定义 `controlOrigin = new URL(controlUrl).origin`、`amd64URL = new URL(amd64.downloadUrl, controlOrigin).toString()` 和 `arm64URL = new URL(arm64.downloadUrl, controlOrigin).toString()`，命令只使用这三个规范化绝对值。

生成结果是可整体粘贴的一段命令，先用当前 Shell 检查 Bash，再通过固定 heredoc 进入 Bash。`buildAgentInstallCommand` 用数组生成完整内容，动态值只经过 `shellQuote` 插入：

```ts
return [
  'if command -v bash >/dev/null 2>&1; then',
  "  bash -s <<'YUNLING_INSTALL'",
  'set -euo pipefail',
  `control_url=${shellQuote(controlOrigin)}`,
  'temp_dir=$(mktemp -d)',
  'cleanup() { status=$?; trap - EXIT; rm -rf "$temp_dir"; exit "$status"; }',
  'trap cleanup EXIT HUP INT TERM',
  'case "$(uname -s):$(uname -m)" in',
  `  Linux:x86_64) download_url=${shellQuote(amd64URL)}; expected_sha256=${shellQuote(amd64.sha256)} ;;`,
  `  Linux:aarch64|Linux:arm64) download_url=${shellQuote(arm64URL)}; expected_sha256=${shellQuote(arm64.sha256)} ;;`,
  "  *) echo '仅支持 Linux x86_64 或 Linux ARM64。' >&2; exit 1 ;;",
  'esac',
  'for required in tar sha256sum; do command -v "$required" >/dev/null 2>&1 || { echo "缺少依赖：$required。安装后请重新执行本命令。" >&2; exit 1; }; done',
  'archive="$temp_dir/yunling-agent.tar.gz"',
  `if command -v curl >/dev/null 2>&1; then curl --proto '=https' --tlsv1.2 --fail --silent --show-error "$download_url" -o "$archive" || { echo '代理安装包下载失败，请检查网络后重试。' >&2; exit 1; }`,
  `elif command -v wget >/dev/null 2>&1; then wget --https-only -qO "$archive" "$download_url" || { echo '代理安装包下载失败，请检查网络后重试。' >&2; exit 1; }`,
  `else echo '缺少依赖：请安装 curl 或 wget 后重试。' >&2; exit 1; fi`,
  'read -r actual_sha256 _ < <(sha256sum "$archive")',
  `[[ "$actual_sha256" == "$expected_sha256" ]] || { echo '代理安装包 SHA-256 校验失败，已停止安装。' >&2; exit 1; }`,
  'seen_files=""; entry_count=0',
  'while IFS= read -r entry; do',
  '  case "$entry" in 50-yunling-agent.rules|install.sh|yunling-agent|yunling-agent.service|yunling-run@.service) ;; *) echo "代理安装包包含未允许的文件：$entry" >&2; exit 1 ;; esac',
  '  case " $seen_files " in *" $entry "*) echo "代理安装包包含重复文件：$entry" >&2; exit 1 ;; esac',
  '  seen_files="$seen_files $entry"; entry_count=$((entry_count + 1))',
  'done < <(tar -tzf "$archive")',
  `[[ "$entry_count" -eq 5 ]] || { echo '代理安装包文件不完整。' >&2; exit 1; }`,
  'extract_dir="$temp_dir/extracted"; mkdir -m 0700 "$extract_dir"',
  `tar -xzf "$archive" --no-same-owner --no-same-permissions -C "$extract_dir" || { echo '代理安装包解压失败，已停止安装。' >&2; exit 1; }`,
  'for required in 50-yunling-agent.rules install.sh yunling-agent yunling-agent.service yunling-run@.service; do [[ -f "$extract_dir/$required" && ! -L "$extract_dir/$required" ]] || { echo "代理安装包文件类型异常：$required" >&2; exit 1; }; done',
  'if [[ "$(id -u)" -eq 0 ]]; then bash "$extract_dir/install.sh" --control-url "$control_url"',
  `else command -v sudo >/dev/null 2>&1 || { echo '缺少 root 权限或 sudo，无法安装代理。' >&2; exit 1; }; sudo -- bash "$extract_dir/install.sh" --control-url "$control_url"; fi`,
  'YUNLING_INSTALL',
  'else',
  "  echo '缺少依赖：请安装 Bash 后重试。' >&2",
  '  false',
  'fi',
].join('\n')
```

`shellQuote` 使用现有单引号转义规则。由于条目白名单只含无空格固定文件名，上面的 `seen_files` 比较不会产生字段分割歧义；五项逐个类型检查同时拒绝缺项和符号链接。

- [ ] **Step 7: 运行并提交**

Run: `npm --workspace apps/web test -- --run src/api/client.test.ts src/features/servers/agentInstallCommand.test.ts`

Expected: PASS。

```bash
git add apps/web/src/api/client.ts apps/web/src/api/client.test.ts apps/web/src/features/servers/agentInstallCommand.ts apps/web/src/features/servers/agentInstallCommand.test.ts
git commit -m "feat: 生成安全代理安装命令"
```

### Task 7: 把向导接入发布状态机

**Files:**
- Modify: `apps/web/src/features/servers/ServerEnrollmentDialog.tsx`
- Modify: `apps/web/src/features/servers/ServersPage.test.tsx`
- Modify: `apps/web/src/app/styles.css:1401-1565,2890-2930`

**Interfaces:**
- Consumes: Task 6 的客户端、类型和命令生成器。
- Produces: `releaseState: 'loading' | 'ready' | 'failed'`、`loadRelease()` 与签发后的关闭确认状态。
- Preserves: 一次显示、复制错误、签发期间关闭保护、焦点约束和管理员权限。

- [ ] **Step 1: 更新成功 fixture**

所有打开向导的 fetch mock 显式处理 `/api/releases/agent/latest`，返回：

```ts
const releaseResponse = {
  version: '0.1.0', artifacts: [
    { os: 'linux', arch: 'amd64', file_name: 'yunling-agent-0.1.0-linux-amd64.tar.gz', byte_size: 100, sha256: 'a'.repeat(64), download_url: `/api/releases/agent/0.1.0/${'a'.repeat(64)}/yunling-agent-0.1.0-linux-amd64.tar.gz` },
    { os: 'linux', arch: 'arm64', file_name: 'yunling-agent-0.1.0-linux-arm64.tar.gz', byte_size: 100, sha256: 'b'.repeat(64), download_url: `/api/releases/agent/0.1.0/${'b'.repeat(64)}/yunling-agent-0.1.0-linux-arm64.tar.gz` },
  ],
}
```

成功测试先等待版本和支持架构，再签发；断言新命令有两个摘要、不含令牌、不含手动上传提示，标题为“一条命令安装并接入”。

- [ ] **Step 2: 写加载门槛与重试测试**

增加：清单 Promise 未完成时显示“正在读取代理版本”且按钮禁用；503 时显示“代理发布暂不可用”和“重新加载”；首次 503、重试成功后允许签发；缺 arm64 时显示固定中文错误且不泄露对象/堆栈。所有路径都断言清单未就绪时 enrollment 请求数为 0。签发后按 Escape、标题栏关闭或“完成并关闭”都先出现“关闭后无法再次查看令牌”的确认区；选择“继续查看”保留 token，选择“确认关闭”才卸载 dialog。

- [ ] **Step 3: 运行确认失败**

Run: `npm --workspace apps/web test -- --run src/features/servers/ServersPage.test.tsx`

Expected: FAIL，现有向导不加载清单且仍提示上传。

- [ ] **Step 4: 实现状态机**

```ts
const [releaseState, setReleaseState] = useState<'loading' | 'ready' | 'failed'>('loading')
const [release, setRelease] = useState<AgentReleaseManifest | null>(null)
const releaseRequestRef = useRef(0)

async function loadRelease() {
  const requestID = ++releaseRequestRef.current
  setReleaseState('loading'); setError('')
  try {
    const next = await getLatestAgentRelease()
    buildAgentInstallCommand(controlUrl, next)
    if (mountedRef.current && requestID === releaseRequestRef.current) { setRelease(next); setReleaseState('ready') }
  } catch (reason) {
    if (mountedRef.current && requestID === releaseRequestRef.current) {
      setRelease(null); setReleaseState('failed')
      setError(chineseError(reason, '代理版本加载失败，请检查网络后重试。'))
    }
  }
}
```

挂载 effect 在恢复 mountedRef 后调用。`submit` 再次检查 ready/release，否则固定中文报错且不调用 enrollment API；按钮 `disabled={creating || releaseState !== 'ready'}`。

- [ ] **Step 5: 更新结果页和样式**

加载用 `role=status`；失败用 `role=alert` 和“重新加载”；就绪显示“代理版本 0.1.0”和“支持 Linux x86_64 / ARM64”。结果页标题改“一条命令安装并接入”，说明自动选架构/下载/校验，删除全部 `/tmp` 上传文字。保留 token 独立显示和复制，命令生成器永不接收 token。新增 `confirmClose` 状态；已签发时 `requestClose` 只打开 `role=alertdialog` 的关闭确认区，确认按钮直接调用 `onClose`，取消按钮关闭确认区并把焦点还给结果标题。

新增 `.enrollment-release-state`，复用现有绿色、边框、圆角、按钮；移动端让状态和重试按钮纵向排列。签发成功结果继续聚焦，创建期间仍不能关闭。

- [ ] **Step 6: 运行并提交**

Run: `npm --workspace apps/web test -- --run src/features/servers/ServersPage.test.tsx src/app/App.test.tsx`

Expected: PASS。

```bash
git add apps/web/src/features/servers/ServerEnrollmentDialog.tsx apps/web/src/features/servers/ServersPage.test.tsx apps/web/src/app/styles.css
git commit -m "feat: 启用一键服务器接入向导"
```

### Task 8: 跨层验证与运维文档

**Files:**
- Modify: `deploy/README.md`
- Review: `deploy/Caddyfile`、`deploy/Dockerfile.services`、`deploy/docker-compose.yml`
- Review: Tasks 1-7 的全部文件

**Interfaces:**
- Consumes: Tasks 1-7 完整实现。
- Produces: 可重复本地构建、下载与排障说明。
- Confirms: 现有 Caddy `/api/*` 已覆盖发布路由，不增加端口。

- [ ] **Step 1: 运行全部测试和构建**

Run: `gofmt -w cmd/agent/main.go cmd/agent/main_test.go cmd/api/main.go cmd/api/main_test.go internal/agentrelease/*.go`

Run: `go test ./... -count=1`

Run: `npm --workspace apps/web test -- --run`

Run: `npm --workspace apps/web run build`

Run: `docker run --rm -v "$PWD:/src" -w /src bash:5.3 bash -n deploy/agent/install.sh`

Run: `docker run --rm -v "$PWD:/src" -w /src bash:5.3 bash deploy/agent/install_test.sh`

Run: `docker run --rm -v "$PWD:/src" -w /src alpine:3.24 sh deploy/agent/package_test.sh`

Expected: 全部成功，日志不含 fixture token。

- [ ] **Step 2: 验证生产形态镜像**

Run: `docker compose --env-file deploy/.env -f deploy/docker-compose.yml build api web`

从临时 API 容器复制 `/opt/yunling/releases/agent` 到临时目录，逐包运行 `sha256sum`、`tar -tzf` 和 `file`。Expected: 哈希与 manifest 一致；每包五个文件；ELF 分别为 x86-64/aarch64；amd64 二进制 `version` 输出 `0.1.0`，arm64 无模拟器时以 ELF 头和链接构建参数为证据。

- [ ] **Step 3: 启动本地路由验收**

启动 Compose API 后用 Python 从清单提取真实值并请求，不手工拼接版本或摘要：

```bash
python3 - <<'PY'
import hashlib, json, urllib.request
base = 'http://127.0.0.1:8080'
manifest = json.load(urllib.request.urlopen(base + '/api/releases/agent/latest'))
for artifact in manifest['artifacts']:
    response = urllib.request.urlopen(base + artifact['download_url'])
    body = response.read()
    assert hashlib.sha256(body).hexdigest() == artifact['sha256']
    assert response.headers['Cache-Control'] == 'public, max-age=31536000, immutable'
print(manifest['version'], len(manifest['artifacts']))
PY
```

另用清单返回的 ETag 发条件请求，断言 304。未登录清单/归档成功，但 POST enrollment token 仍未认证。

- [ ] **Step 4: 更新 `deploy/README.md`**

记录 `AGENT_VERSION`、发布目录、两个 GET 路由、缓存语义、安全边界、支持平台和依赖。写明清单 503、下载失败、哈希失败、已有凭据、令牌过期的排查顺序；明确禁止把 token 放入命令、环境变量、URL、聊天或工单。

- [ ] **Step 5: 检查差异和敏感信息**

Run: `git diff --check`

Run: `rg -n "AKID|SecretKey|chaomai|951125" --glob '!docs/superpowers/**' .`

Expected: diff check 无输出，仓库没有用户真实凭据。

- [ ] **Step 6: 提交文档**

```bash
git add deploy/README.md
git commit -m "docs: 说明一键代理接入运维流程"
```

### Task 9: 生产发布与无副作用验收

**Files:**
- Modify after verification: `deploy/PRODUCTION.md`

**Interfaces:**
- Consumes: Task 8 全部通过的提交和两个哈希。
- Produces: 腾讯云生产 API/Web 新镜像、公开下载证据和回滚记录。
- Preserves: 现有京东云代理身份、任务调度、队列和运行数据。

- [ ] **Step 1: 建立发布包和恢复点**

本地先确认工作区干净，用 `git archive HEAD` 生成带短提交号的 tar.gz 并计算 SHA-256，上传到腾讯云普通用户主目录。生产 `/opt/yunling` 先执行 `backup=/opt/backups/yunling-before-agent-release-$(date +%Y%m%d-%H%M%S).tar.gz`，再把仓库文件与 Compose 配置备份到该精确路径并记录摘要；备份/日志不显示 `deploy/.env` 或 `deploy/secrets` 内容。

- [ ] **Step 2: 解包并只重建 API/Web**

在 `/opt/yunling` 核对上传包摘要后解包：

```bash
sudo docker compose --env-file deploy/.env -f deploy/docker-compose.yml build api web
sudo docker compose --env-file deploy/.env -f deploy/docker-compose.yml up -d --no-deps api web
sudo docker compose --env-file deploy/.env -f deploy/docker-compose.yml ps api web scheduler ops
```

Expected: API/Web 新容器健康；Scheduler/Ops 未重建、未重启。

- [ ] **Step 3: 验证健康和现有代理**

```bash
curl -fsS http://127.0.0.1:8080/api/health
curl -fsS https://aiwise.top/healthz
sudo docker compose --env-file deploy/.env -f deploy/docker-compose.yml logs --tail=100 api web
```

Expected: 健康接口返回 `ok`；日志有“代理发布已就绪”且无凭据；控制台中“京东云执行节点-1”保持在线或一个正常心跳周期内恢复。

- [ ] **Step 4: 公网核对两个安装包**

使用 Task 8 的 Python 验证脚本，把 base 改为 `https://aiwise.top`；再检查 Content-Type、Content-Disposition、ETag、文件集合和 ELF 架构。未登录 GET 成功，未登录 POST enrollment token 被拒绝。

- [ ] **Step 5: 浏览器验收但不创建测试令牌**

管理员进入服务器向导，确认加载状态、版本 `0.1.0`、两种架构、就绪前按钮禁用以及无 `/tmp` 上传说明。没有备用真实服务器时不点击创建令牌。第一次新增真实服务器时再完成安装、注册、心跳、资源、同步和任务接收端到端验收。

- [ ] **Step 6: 记录并提交生产证据**

`deploy/PRODUCTION.md` 记录部署日期、功能提交、API/Web 镜像 ID、代理版本、两个包的文件名/字节数/SHA-256、公网状态、现有代理状态、恢复点和摘要；不得记录 token、密码、云密钥或环境内容。

```bash
git add deploy/PRODUCTION.md
git commit -m "docs: 记录一键代理接入生产发布"
```

- [ ] **Step 7: 最终核验**

Run: `git status --short --branch`

Run: `git log --oneline -10`

Expected: 工作区干净；功能、测试、文档和生产记录提交齐全；生产系统健康。
