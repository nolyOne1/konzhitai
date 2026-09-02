import type { AgentReleaseArtifact, AgentReleaseManifest } from '../../api/client'

const RELEASE_ERROR = '代理发布清单不完整，请重新加载。'
const VERSION_PATTERN = /^[0-9A-Za-z][0-9A-Za-z._-]{0,63}$/u
const FILE_NAME_PATTERN = /^[0-9A-Za-z][0-9A-Za-z._-]{0,191}\.tar\.gz$/u
const SHA256_PATTERN = /^[0-9a-f]{64}$/u

export function buildAgentInstallCommand(controlUrl: string, release: AgentReleaseManifest): string {
  const controlOrigin = validatedControlOrigin(controlUrl)
  const artifacts = validateRelease(controlOrigin, release)
  const amd64 = artifacts.get('amd64')!
  const arm64 = artifacts.get('arm64')!
  const amd64URL = new URL(amd64.downloadUrl, controlOrigin).toString()
  const arm64URL = new URL(arm64.downloadUrl, controlOrigin).toString()

  return [
    'if command -v bash >/dev/null 2>&1; then',
    "  bash -s <<'YUNLING_INSTALL'",
    'set -euo pipefail',
    `control_url=${shellQuote(controlOrigin)}`,
    "command -v mktemp >/dev/null 2>&1 || { echo '缺少依赖：mktemp。安装后请重新执行本命令。' >&2; exit 1; }",
    "temp_dir=$(mktemp -d) || { echo '无法创建安全临时目录，已停止安装。' >&2; exit 1; }",
    'cleanup() { status=$?; trap - EXIT HUP INT TERM; rm -rf "$temp_dir"; exit "$status"; }',
    'abort() { status="$1"; trap - EXIT HUP INT TERM; rm -rf "$temp_dir"; exit "$status"; }',
    'trap cleanup EXIT',
    "trap 'abort 129' HUP",
    "trap 'abort 130' INT",
    "trap 'abort 143' TERM",
    'case "$(uname -s):$(uname -m)" in',
    `  Linux:x86_64) download_url=${shellQuote(amd64URL)}; expected_sha256=${shellQuote(amd64.sha256)} ;;`,
    `  Linux:aarch64|Linux:arm64) download_url=${shellQuote(arm64URL)}; expected_sha256=${shellQuote(arm64.sha256)} ;;`,
    "  *) echo '仅支持 Linux x86_64 或 Linux ARM64。' >&2; exit 1 ;;",
    'esac',
    'for required in tar sha256sum; do',
    '  command -v "$required" >/dev/null 2>&1 || { echo "缺少依赖：$required。安装后请重新执行本命令。" >&2; exit 1; }',
    'done',
    'archive="$temp_dir/yunling-agent.tar.gz"',
    'if command -v curl >/dev/null 2>&1; then',
    "  curl --proto '=https' --tlsv1.2 --fail --silent --show-error \"$download_url\" -o \"$archive\" || { echo '代理安装包下载失败，请检查网络后重试。' >&2; exit 1; }",
    'elif command -v wget >/dev/null 2>&1; then',
    "  wget --https-only -qO \"$archive\" \"$download_url\" || { echo '代理安装包下载失败，请检查网络后重试。' >&2; exit 1; }",
    'else',
    "  echo '缺少依赖：请安装 curl 或 wget 后重试。' >&2",
    '  exit 1',
    'fi',
    'read -r actual_sha256 _ < <(sha256sum "$archive")',
    "[[ \"$actual_sha256\" == \"$expected_sha256\" ]] || { echo '代理安装包 SHA-256 校验失败，已停止安装。' >&2; exit 1; }",
    'seen_files=""; entry_count=0',
    'while IFS= read -r entry; do',
    '  case "$entry" in 50-yunling-agent.rules|install.sh|yunling-agent|yunling-agent.service|yunling-run@.service) ;; *) echo "代理安装包包含未允许的文件：$entry" >&2; exit 1 ;; esac',
    '  case " $seen_files " in *" $entry "*) echo "代理安装包包含重复文件：$entry" >&2; exit 1 ;; esac',
    '  seen_files="$seen_files $entry"; entry_count=$((entry_count + 1))',
    'done < <(tar -tzf "$archive")',
    "[[ \"$entry_count\" -eq 5 ]] || { echo '代理安装包文件不完整。' >&2; exit 1; }",
    'extract_dir="$temp_dir/extracted"',
    'mkdir -m 0700 "$extract_dir"',
    "tar -xzf \"$archive\" --no-same-owner --no-same-permissions -C \"$extract_dir\" || { echo '代理安装包解压失败，已停止安装。' >&2; exit 1; }",
    'for required in 50-yunling-agent.rules install.sh yunling-agent yunling-agent.service yunling-run@.service; do',
    '  [[ -f "$extract_dir/$required" && ! -L "$extract_dir/$required" ]] || { echo "代理安装包文件类型异常：$required" >&2; exit 1; }',
    'done',
    'if [[ "$(id -u)" -eq 0 ]]; then',
    '  bash "$extract_dir/install.sh" --control-url "$control_url" </dev/tty',
    'else',
    "  command -v sudo >/dev/null 2>&1 || { echo '缺少 root 权限或 sudo，无法安装代理。' >&2; exit 1; }",
    '  sudo -- bash "$extract_dir/install.sh" --control-url "$control_url" </dev/tty',
    'fi',
    'YUNLING_INSTALL',
    'else',
    "  echo '缺少依赖：请安装 Bash 后重试。' >&2",
    '  false',
    'fi',
  ].join('\n')
}

function validatedControlOrigin(controlUrl: string): string {
  try {
    const parsed = new URL(controlUrl)
    if (parsed.protocol !== 'https:' || parsed.username || parsed.password || parsed.origin === 'null') throw new Error(RELEASE_ERROR)
    return parsed.origin
  } catch {
    throw new Error(RELEASE_ERROR)
  }
}

function validateRelease(controlOrigin: string, release: AgentReleaseManifest): Map<string, AgentReleaseArtifact> {
  if (!release || !VERSION_PATTERN.test(release.version) || !Array.isArray(release.artifacts) || release.artifacts.length !== 2) {
    throw new Error(RELEASE_ERROR)
  }
  const artifacts = new Map<string, AgentReleaseArtifact>()
  for (const artifact of release.artifacts) {
    if (!artifact || artifact.os !== 'linux' || (artifact.arch !== 'amd64' && artifact.arch !== 'arm64') || artifacts.has(artifact.arch)) {
      throw new Error(RELEASE_ERROR)
    }
    if (!FILE_NAME_PATTERN.test(artifact.fileName) || !SHA256_PATTERN.test(artifact.sha256) ||
      !Number.isSafeInteger(artifact.byteSize) || artifact.byteSize <= 0) {
      throw new Error(RELEASE_ERROR)
    }
    const expectedPath = `/api/releases/agent/${release.version}/${artifact.sha256}/${artifact.fileName}`
    if (artifact.downloadUrl !== expectedPath || !artifact.downloadUrl.startsWith('/api/releases/agent/') || artifact.downloadUrl.startsWith('//')) {
      throw new Error(RELEASE_ERROR)
    }
    let parsed: URL
    try {
      parsed = new URL(artifact.downloadUrl, controlOrigin)
    } catch {
      throw new Error(RELEASE_ERROR)
    }
    if (parsed.origin !== controlOrigin || parsed.pathname !== expectedPath || parsed.search || parsed.hash) {
      throw new Error(RELEASE_ERROR)
    }
    artifacts.set(artifact.arch, artifact)
  }
  if (!artifacts.has('amd64') || !artifacts.has('arm64')) throw new Error(RELEASE_ERROR)
  return artifacts
}

function shellQuote(value: string) {
  return `'${value.replaceAll("'", `'"'"'`)}'`
}
