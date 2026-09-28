import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { createAgentUpgradePlan, type AgentRelease, type ServerView } from '../../api/client'
import { AgentUpgradeDialog } from './AgentUpgradeDialog'

vi.mock('../../api/client', async (importOriginal) => ({ ...(await importOriginal<typeof import('../../api/client')>()), createAgentUpgradePlan: vi.fn() }))

const releases: AgentRelease[] = [{ id: 'release-2', version: '0.2.0', status: 'available', recommended: true, releaseNotes: '稳定版', capabilities: ['self_upgrade_v1'], createdAt: '2026-09-07T00:00:00Z', artifacts: [{ os: 'linux', arch: 'amd64', fileName: 'agent.tar.gz', byteSize: 42, sha256: 'a'.repeat(64), downloadUrl: '/download' }] }]
const baseServer: ServerView = { id: 'server-1', name: '京东云执行节点-1', cloudProvider: '京东云', region: '华东 1', status: 'online', enabled: true, draining: false, labels: { 用途: '批处理' }, runtimes: ['bash'], agentVersion: '0.1.0', agentOS: 'linux', agentArch: 'amd64', agentCapabilities: ['self_upgrade_v1'], schedulingWeight: 100, cpuUsagePercent: 10, memoryTotalBytes: 8_000, memoryAvailableBytes: 4_000, diskTotalBytes: 10_000, diskAvailableBytes: 5_000, runningTasks: 0, lastSeenAt: '2026-09-07T00:00:00Z' }

describe('升级计划向导', () => {
  beforeEach(() => vi.mocked(createAgentUpgradePlan).mockReset().mockResolvedValue({ id: 'plan-1', targetVersion: '0.2.0', status: 'running', currentBatch: 1, targets: [], events: [], createdAt: '2026-09-07T00:00:00Z', pauseReason: '' }))

  it('创建首批单节点的分批升级计划', async () => {
    const onCreated = vi.fn()
    render(<AgentUpgradeDialog releases={releases} servers={[baseServer, { ...baseServer, id: 'legacy', name: '旧代理节点', agentCapabilities: [] }]} onClose={vi.fn()} onCreated={onCreated} />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('radio', { name: /0.2.0/ }))
    await user.click(screen.getByRole('button', { name: '下一步' }))
    expect(screen.getByLabelText(/旧代理节点/)).toBeDisabled()
    await user.click(screen.getByLabelText(/京东云执行节点-1/))
    await user.click(screen.getByRole('button', { name: '下一步' }))
    expect(screen.getByText('首批固定 1 台')).toBeVisible()
    await user.clear(screen.getByLabelText('后续每批服务器数'))
    await user.type(screen.getByLabelText('后续每批服务器数'), '2')
    await user.click(screen.getByRole('button', { name: '启动升级计划' }))
    expect(createAgentUpgradePlan).toHaveBeenCalledWith({ targetReleaseId: 'release-2', serverIds: ['server-1'], batchSize: 2, drainTimeoutSeconds: 3600, reconnectTimeoutSeconds: 120, verificationSeconds: 30 })
    expect(onCreated).toHaveBeenCalled()
  })

  it('可按云厂商、地域、标签和版本筛选节点', async () => {
    render(<AgentUpgradeDialog releases={releases} servers={[baseServer, { ...baseServer, id: 'server-2', name: '腾讯云节点', cloudProvider: '腾讯云', region: '华北', labels: { 用途: '接口' } }]} onClose={vi.fn()} onCreated={vi.fn()} />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('radio', { name: /0.2.0/ }))
    await user.click(screen.getByRole('button', { name: '下一步' }))
    await user.selectOptions(screen.getByLabelText('云厂商筛选'), '京东云')
    await user.type(screen.getByLabelText('地域筛选'), '华东 1')
    await user.type(screen.getByLabelText('标签筛选'), '用途=批处理')
    await user.type(screen.getByLabelText('当前版本筛选'), '0.1.0')
    expect(screen.getByLabelText(/京东云执行节点-1/)).toBeVisible()
    expect(screen.queryByLabelText(/腾讯云节点/)).not.toBeInTheDocument()
  })

  it('推荐 0.2.0 不允许当前 0.2.6 降级，相等或无法比较的版本显示禁选原因', async () => {
    const cases = [
      ['higher', '高版本节点', '0.2.6', '高于目标版本'],
      ['equal', '相同版本节点', 'v0.2.0', '已是目标版本'],
      ['unknown', '未知节点', '', '版本无法比较'],
      ['custom', '自定义节点', 'custom', '版本无法比较'],
      ['prerelease', '预发布节点', '0.1.0-rc.1', '版本无法比较'],
    ]
    render(<AgentUpgradeDialog releases={releases} servers={[baseServer, ...cases.map(([id, name, agentVersion]) => ({ ...baseServer, id, name, agentVersion }))]} onClose={vi.fn()} onCreated={vi.fn()} />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('radio', { name: /0.2.0/ }))
    await user.click(screen.getByRole('button', { name: '下一步' }))
    for (const [, name, , reason] of cases) {
      const checkbox = screen.getByRole('checkbox', { name: new RegExp(name) })
      expect(checkbox).toBeDisabled()
      expect(within(checkbox.closest('label')!).getByText(reason)).toBeVisible()
    }
    expect(screen.getByRole('checkbox', { name: /京东云执行节点-1/ })).toBeEnabled()
    expect(createAgentUpgradePlan).not.toHaveBeenCalled()
  })

  it('按数字段比较 0.2.9 与 0.2.10，并能提交正常高版本升级', async () => {
    render(<AgentUpgradeDialog releases={[{ ...releases[0], version: '0.2.10' }]} servers={[{ ...baseServer, agentVersion: 'v0.2.9' }]} onClose={vi.fn()} onCreated={vi.fn()} />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('radio', { name: /0.2.10/ }))
    await user.click(screen.getByRole('button', { name: '下一步' }))
    await user.click(screen.getByRole('checkbox', { name: /京东云执行节点-1/ }))
    await user.click(screen.getByRole('button', { name: '下一步' }))
    await user.click(screen.getByRole('button', { name: '启动升级计划' }))
    expect(createAgentUpgradePlan).toHaveBeenCalledWith(expect.objectContaining({ targetReleaseId: 'release-2', serverIds: ['server-1'] }))
  })

  it.each(['0.2.6', 'unknown'])('确认前节点变为 %s 时重新筛选并阻止提交', async (agentVersion) => {
    const props = { releases, servers: [baseServer], onClose: vi.fn(), onCreated: vi.fn() }
    const { rerender } = render(<AgentUpgradeDialog {...props} />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('radio', { name: /0.2.0/ }))
    await user.click(screen.getByRole('button', { name: '下一步' }))
    await user.click(screen.getByRole('checkbox', { name: /京东云执行节点-1/ }))
    await user.click(screen.getByRole('button', { name: '下一步' }))
    rerender(<AgentUpgradeDialog {...props} servers={[{ ...baseServer, agentVersion }]} />)
    await user.click(screen.getByRole('button', { name: '启动升级计划' }))
    expect(screen.getByRole('alert')).toHaveTextContent('所选服务器已不再符合升级条件')
    expect(screen.getByRole('checkbox', { name: /京东云执行节点-1/ })).toBeDisabled()
    expect(createAgentUpgradePlan).not.toHaveBeenCalled()
  })

  it('确认前目标版本撤回时阻止提交并返回选择版本', async () => {
    const props = { releases, servers: [baseServer], onClose: vi.fn(), onCreated: vi.fn() }
    const { rerender } = render(<AgentUpgradeDialog {...props} />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('radio', { name: /0.2.0/ }))
    await user.click(screen.getByRole('button', { name: '下一步' }))
    await user.click(screen.getByRole('checkbox', { name: /京东云执行节点-1/ }))
    await user.click(screen.getByRole('button', { name: '下一步' }))
    rerender(<AgentUpgradeDialog {...props} releases={[{ ...releases[0], status: 'withdrawn' }]} />)
    await user.click(screen.getByRole('button', { name: '启动升级计划' }))
    expect(screen.getByRole('heading', { name: '选择目标版本' })).toBeVisible()
    expect(screen.getByRole('alert')).toHaveTextContent('请选择目标版本')
    expect(createAgentUpgradePlan).not.toHaveBeenCalled()
  })

  it('在超时参数旁显示边界错误', async () => {
    render(<AgentUpgradeDialog releases={releases} servers={[baseServer]} onClose={vi.fn()} onCreated={vi.fn()} />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('radio', { name: /0.2.0/ })); await user.click(screen.getByRole('button', { name: '下一步' })); await user.click(screen.getByLabelText(/京东云执行节点-1/)); await user.click(screen.getByRole('button', { name: '下一步' }))
    await user.clear(screen.getByLabelText('排空超时（秒）')); await user.type(screen.getByLabelText('排空超时（秒）'), '10')
    await user.click(screen.getByRole('button', { name: '启动升级计划' }))
    expect(within(screen.getByLabelText('排空超时（秒）').closest('.form-field')!).getByRole('alert')).toHaveTextContent('60 到 86400')
  })

  it('键盘焦点保持在升级向导内', async () => {
    render(<AgentUpgradeDialog releases={releases} servers={[baseServer]} onClose={vi.fn()} onCreated={vi.fn()} />)
    const user = userEvent.setup()
    const close = screen.getByRole('button', { name: '关闭升级向导' })
    close.focus()
    await user.keyboard('{Shift>}{Tab}{/Shift}')
    expect(screen.getByRole('button', { name: '下一步' })).toHaveFocus()
    await user.tab()
    expect(close).toHaveFocus()
  })
})
