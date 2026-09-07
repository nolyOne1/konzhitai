import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'

import type { ServerView } from '../../api/client'
import { ServerVersionStatus } from './ServerVersionStatus'

function server(overrides: Partial<ServerView> = {}): ServerView {
  return {
    id: 'server-1', name: '京东云执行节点-1', cloudProvider: '京东云', region: '华北',
    status: 'online', enabled: true, draining: false, labels: {}, runtimes: ['bash'],
    agentVersion: '0.1.0', agentOS: 'linux', agentArch: 'amd64', agentCapabilities: ['self_upgrade_v1'],
    schedulingWeight: 100, cpuUsagePercent: 20, memoryTotalBytes: 8_589_934_592,
    memoryAvailableBytes: 4_294_967_296, diskTotalBytes: 107_374_182_400,
    diskAvailableBytes: 75_161_927_680, runningTasks: 0, lastSeenAt: '2026-09-07T00:00:00Z',
    ...overrides,
  }
}

describe('服务器代理版本状态', () => {
  it('旧代理显示需人工升级基线', () => {
    render(<ServerVersionStatus server={server({ agentCapabilities: [] })} recommendedVersion="0.2.0" />)
    expect(screen.getByText('需人工升级基线')).toBeVisible()
  })

  it.each([
    ['0.2.0', ['self_upgrade_v1'], undefined, '已是最新版'],
    ['0.1.0', ['self_upgrade_v1'], undefined, '可升级'],
    ['0.1.0', ['self_upgrade_v1'], 'installing', '升级中'],
    ['', ['self_upgrade_v1'], undefined, '版本未知'],
    ['0.1.0', ['self_upgrade_v1'], 'manual_intervention', '升级失败'],
  ])('版本 %s、升级阶段 %s 时显示 %s', (version, capabilities, upgradeStatus, expected) => {
    render(<ServerVersionStatus server={server({ agentVersion: version, agentCapabilities: capabilities, upgradeStatus })} recommendedVersion="0.2.0" />)
    expect(screen.getByText(expected)).toBeVisible()
  })
})
