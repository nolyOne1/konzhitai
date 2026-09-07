import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { getAgentReleases, getAgentUpgradePlans, getServers, getSession, withdrawAgentRelease, type ServerView } from '../../api/client'
import { AgentUpgradesPage } from './AgentUpgradesPage'

vi.mock('../../api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../api/client')>()
  return { ...actual, getAgentReleases: vi.fn(), getAgentUpgradePlans: vi.fn(), getServers: vi.fn(), getSession: vi.fn(), withdrawAgentRelease: vi.fn(), recommendAgentRelease: vi.fn() }
})

const release = (version: string, recommended = false) => ({ id: `release-${version}`, version, status: 'available' as const, recommended, releaseNotes: '稳定版', capabilities: ['self_upgrade_v1'], createdAt: '2026-09-07T00:00:00Z', artifacts: [] })
const server = (id: string, version: string, upgradeStatus = ''): ServerView => ({
  id, name: id, cloudProvider: '京东云', region: '华东 1', status: 'online', enabled: true,
  draining: false, labels: {}, runtimes: ['bash'], agentVersion: version, agentOS: 'linux',
  agentArch: 'amd64', agentCapabilities: ['self_upgrade_v1'], upgradeStatus, schedulingWeight: 100,
  cpuUsagePercent: 0, memoryTotalBytes: 0, memoryAvailableBytes: 0, diskTotalBytes: 0,
  diskAvailableBytes: 0, runningTasks: 0, lastSeenAt: '2026-09-07T00:00:00Z',
})

describe('代理升级工作台', () => {
  beforeEach(() => {
    vi.mocked(getAgentReleases).mockReset().mockResolvedValue([release('0.2.0', true)])
    vi.mocked(getAgentUpgradePlans).mockReset().mockResolvedValue([])
    vi.mocked(getServers).mockReset().mockResolvedValue([])
    vi.mocked(getSession).mockReset().mockResolvedValue({ id: 'admin-1', displayName: '管理员', email: 'admin@example.com', roles: ['admin'], mustChangePassword: false })
    vi.mocked(withdrawAgentRelease).mockReset().mockResolvedValue(undefined)
  })

  it('显示代理升级概况和空状态', async () => {
    render(<AgentUpgradesPage />)
    expect(await screen.findByRole('heading', { level: 1, name: '代理升级' })).toBeVisible()
    expect(screen.getAllByText('推荐版本')[0]).toBeVisible()
    expect(screen.getByText('当前没有进行中的升级计划')).toBeVisible()
    expect(screen.getByRole('button', { name: '创建升级计划' })).toBeEnabled()
  })

  it('汇总服务器总数、待升级、升级中和异常数量', async () => {
    vi.mocked(getServers).mockResolvedValue([
      server('已更新节点', '0.2.0'), server('待升级节点', '0.1.0'),
      server('升级中节点', '0.1.0', 'installing'), server('异常节点', '0.1.0', 'manual_intervention'),
    ])
    render(<AgentUpgradesPage />)
    await screen.findByRole('heading', { level: 1, name: '代理升级' })
    const summary = screen.getByLabelText('升级概况')
    expect(within(summary).getByText('服务器总数').nextElementSibling).toHaveTextContent('4')
    expect(within(summary).getByText('待升级').nextElementSibling).toHaveTextContent('1')
    expect(within(summary).getByText('升级中').nextElementSibling).toHaveTextContent('1')
    expect(within(summary).getByText('异常').nextElementSibling).toHaveTextContent('1')
  })

  it('允许撤回非推荐版本但保护推荐版本', async () => {
    vi.mocked(getAgentReleases).mockResolvedValue([release('0.2.0', true), release('0.1.0')])
    render(<AgentUpgradesPage />)
    const oldRow = await screen.findByRole('row', { name: /0.1.0/ })
    await userEvent.setup().click(within(oldRow).getByRole('button', { name: '撤回版本' }))
    expect(withdrawAgentRelease).toHaveBeenCalledWith('release-0.1.0')
    const currentRow = screen.getByRole('row', { name: /0.2.0/ })
    expect(within(currentRow).queryByRole('button', { name: '撤回版本' })).not.toBeInTheDocument()
  })

  it('加载失败时显示可重试的中文反馈', async () => {
    vi.mocked(getAgentUpgradePlans).mockRejectedValueOnce(new Error('升级服务暂时不可用'))
    render(<AgentUpgradesPage />)
    expect(await screen.findByRole('alert')).toHaveTextContent('升级服务暂时不可用')
    expect(screen.getByRole('button', { name: '重新加载' })).toBeEnabled()
  })

  it('普通成员只能查看升级状态', async () => {
    vi.mocked(getSession).mockResolvedValue({ id: 'viewer-1', displayName: '观察员', email: 'viewer@example.com', roles: ['viewer'], mustChangePassword: false })
    vi.mocked(getAgentReleases).mockResolvedValue([release('0.2.0', true), release('0.1.0')])
    render(<AgentUpgradesPage />)
    expect(await screen.findByText('当前账号可以查看升级盘点和进度；计划控制仅限管理员。')).toBeVisible()
    expect(screen.queryByRole('button', { name: '创建升级计划' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '撤回版本' })).not.toBeInTheDocument()
  })
})
