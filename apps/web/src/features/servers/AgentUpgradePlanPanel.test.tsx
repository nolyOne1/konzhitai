import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { getAgentUpgradePlan, pauseAgentUpgradePlan, retryAgentUpgradeTarget, rollbackAgentUpgradeTarget, type AgentUpgradePlan } from '../../api/client'
import { AgentUpgradePlanPanel } from './AgentUpgradePlanPanel'

vi.mock('../../api/client', async (importOriginal) => ({ ...(await importOriginal<typeof import('../../api/client')>()), getAgentUpgradePlan: vi.fn(), pauseAgentUpgradePlan: vi.fn(), resumeAgentUpgradePlan: vi.fn(), cancelAgentUpgradePlan: vi.fn(), retryAgentUpgradeTarget: vi.fn(), rollbackAgentUpgradeTarget: vi.fn() }))

function plan(status = 'draining', planStatus: AgentUpgradePlan['status'] = 'running'): AgentUpgradePlan {
  return { id: 'plan-1', targetVersion: '0.2.0', status: planStatus, currentBatch: 1, createdAt: '2026-09-07T00:00:00Z', pauseReason: '', events: [{ id: 'event-1', targetId: 'target-1', stage: status, message: '节点已进入升级流程', occurredAt: '2026-09-07T00:01:00Z' }], targets: [{ id: 'target-1', serverId: 'server-1', serverName: '京东云执行节点-1', batchNumber: 1, sourceVersion: '0.1.0', targetVersion: '0.2.0', status, attempts: 1, errorMessage: '', updatedAt: '2026-09-07T00:01:00Z' }] }
}

describe('升级计划详情', () => {
  beforeEach(() => {
    vi.mocked(getAgentUpgradePlan).mockReset().mockResolvedValue(plan())
    vi.mocked(pauseAgentUpgradePlan).mockReset().mockResolvedValue(plan('draining', 'paused'))
    vi.mocked(retryAgentUpgradeTarget).mockReset().mockResolvedValue(plan())
    vi.mocked(rollbackAgentUpgradeTarget).mockReset().mockResolvedValue(plan())
  })

  it.each([
    ['draining', '等待任务结束'], ['downloading', '下载安装包'], ['verifying', '校验安装包'], ['installing', '安装新版本'],
    ['reconnecting', '等待代理重连'], ['health_checking', '健康验证'], ['rolling_back', '正在回滚'], ['manual_intervention', '需要人工处理'],
  ])('状态 %s 显示中文阶段', (status, label) => {
    render(<AgentUpgradePlanPanel plan={plan(status)} onUpdated={vi.fn()} />)
    expect(screen.getAllByText(label)[0]).toBeVisible()
  })

  it('操作处理中禁用按钮并在完成后刷新计划', async () => {
    let finish!: (value: AgentUpgradePlan) => void
    vi.mocked(pauseAgentUpgradePlan).mockReturnValue(new Promise((resolve) => { finish = resolve }))
    vi.mocked(getAgentUpgradePlan).mockResolvedValue(plan('draining', 'paused'))
    render(<AgentUpgradePlanPanel plan={plan()} onUpdated={vi.fn()} />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: '暂停计划' }))
    expect(screen.getByRole('button', { name: '处理中…' })).toBeDisabled()
    finish(plan('draining', 'paused'))
    expect(await screen.findByRole('button', { name: '继续计划' })).toBeEnabled()
    expect(getAgentUpgradePlan).toHaveBeenCalledWith('plan-1')
  })

  it('成功节点可回滚，已回滚节点可重试', async () => {
    const { rerender } = render(<AgentUpgradePlanPanel plan={plan('succeeded', 'succeeded')} onUpdated={vi.fn()} />)
    await userEvent.setup().click(screen.getByRole('button', { name: '回滚此节点' }))
    expect(rollbackAgentUpgradeTarget).toHaveBeenCalledWith('plan-1', 'target-1')
    rerender(<AgentUpgradePlanPanel plan={plan('rolled_back', 'paused')} onUpdated={vi.fn()} />)
    await userEvent.setup().click(screen.getByRole('button', { name: '重试此节点' }))
    expect(retryAgentUpgradeTarget).toHaveBeenCalledWith('plan-1', 'target-1')
  })
})
