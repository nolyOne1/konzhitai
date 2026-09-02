import { describe, expect, it } from 'vitest'

import type { AgentReleaseManifest } from '../../api/client'
import { buildAgentInstallCommand } from './agentInstallCommand'

const RELEASE_ERROR = '代理发布清单不完整，请重新加载。'

describe('代理一条命令安装', () => {
  it('生成先校验后提权且不含注册令牌的双架构命令', () => {
    const release = validRelease()
    const command = buildAgentInstallCommand('https://aiwise.top/ignored-path', release)

    for (const expected of [
      "bash -s <<'YUNLING_INSTALL'",
      'Linux:x86_64)',
      'Linux:aarch64|Linux:arm64)',
      'mktemp -d',
      'trap cleanup EXIT HUP INT TERM',
      'sha256sum',
      'curl',
      'wget',
      'tar -tzf "$archive"',
      '--no-same-owner --no-same-permissions',
      'sudo -- bash "$extract_dir/install.sh" --control-url "$control_url"',
      `https://aiwise.top${release.artifacts[0].downloadUrl}`,
      `https://aiwise.top${release.artifacts[1].downloadUrl}`,
      release.artifacts[0].sha256,
      release.artifacts[1].sha256,
    ]) {
      expect(command).toContain(expected)
    }
    expect(command.indexOf('sha256sum "$archive"')).toBeLessThan(command.indexOf('sudo -- bash'))
    expect(command).not.toContain('YUNLING_ENROLLMENT_TOKEN')
    expect(command).not.toContain('/tmp/install.sh')
    expect(command).not.toContain('/tmp/yunling-agent')
    expect(command).not.toContain('test-token-must-not-appear')
  })

  it.each([
    ['缺少 arm64', (release: AgentReleaseManifest) => { release.artifacts = release.artifacts.slice(0, 1) }],
    ['未知架构', (release: AgentReleaseManifest) => { release.artifacts[1].arch = 'ppc64le' }],
    ['重复架构', (release: AgentReleaseManifest) => { release.artifacts[1].arch = 'amd64' }],
    ['非 Linux', (release: AgentReleaseManifest) => { release.artifacts[0].os = 'windows' }],
    ['非法版本', (release: AgentReleaseManifest) => { release.version = 'bad/version' }],
    ['错误摘要', (release: AgentReleaseManifest) => { release.artifacts[0].sha256 = 'ABC' }],
    ['非正字节数', (release: AgentReleaseManifest) => { release.artifacts[0].byteSize = 0 }],
    ['非整数字节数', (release: AgentReleaseManifest) => { release.artifacts[0].byteSize = 1.5 }],
    ['文件名包含路径', (release: AgentReleaseManifest) => { release.artifacts[0].fileName = 'nested/agent.tar.gz' }],
    ['绝对下载 URL', (release: AgentReleaseManifest) => { release.artifacts[0].downloadUrl = `https://aiwise.top${release.artifacts[0].downloadUrl}` }],
    ['协议相对下载 URL', (release: AgentReleaseManifest) => { release.artifacts[0].downloadUrl = `//evil.example${release.artifacts[0].downloadUrl}` }],
    ['跨源下载 URL', (release: AgentReleaseManifest) => { release.artifacts[0].downloadUrl = `https://evil.example${release.artifacts[0].downloadUrl}` }],
    ['URL 版本不一致', (release: AgentReleaseManifest) => { release.artifacts[0].downloadUrl = release.artifacts[0].downloadUrl.replace('/0.1.0/', '/9.9.9/') }],
    ['URL 摘要不一致', (release: AgentReleaseManifest) => { release.artifacts[0].downloadUrl = release.artifacts[0].downloadUrl.replace(release.artifacts[0].sha256, 'f'.repeat(64)) }],
    ['URL 文件名不一致', (release: AgentReleaseManifest) => { release.artifacts[0].downloadUrl = release.artifacts[0].downloadUrl.replace(release.artifacts[0].fileName, 'other.tar.gz') }],
    ['URL 带查询参数', (release: AgentReleaseManifest) => { release.artifacts[0].downloadUrl += '?download=1' }],
  ])('拒绝%s', (_name, mutate) => {
    const release = validRelease()
    mutate(release)
    expect(() => buildAgentInstallCommand('https://aiwise.top', release)).toThrow(RELEASE_ERROR)
  })

  it.each(['http://aiwise.top', 'javascript:alert(1)', 'https://user@aiwise.top'])('拒绝不安全控制平面地址 %s', (controlUrl) => {
    expect(() => buildAgentInstallCommand(controlUrl, validRelease())).toThrow(RELEASE_ERROR)
  })
})

function validRelease(): AgentReleaseManifest {
  const version = '0.1.0'
  const amd64Digest = 'a'.repeat(64)
  const arm64Digest = 'b'.repeat(64)
  return {
    version,
    artifacts: [
      {
        os: 'linux', arch: 'amd64', fileName: `yunling-agent-${version}-linux-amd64.tar.gz`,
        byteSize: 1024, sha256: amd64Digest,
        downloadUrl: `/api/releases/agent/${version}/${amd64Digest}/yunling-agent-${version}-linux-amd64.tar.gz`,
      },
      {
        os: 'linux', arch: 'arm64', fileName: `yunling-agent-${version}-linux-arm64.tar.gz`,
        byteSize: 2048, sha256: arm64Digest,
        downloadUrl: `/api/releases/agent/${version}/${arm64Digest}/yunling-agent-${version}-linux-arm64.tar.gz`,
      },
    ],
  }
}
