import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { ServersPage } from './ServersPage'

describe('服务器管理', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('展示资源、标签和排空操作，并可打开详情', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({
        servers: [{
          id: 'server-1',
          name: '京东云执行节点-1',
          cloudProvider: '京东云',
          region: '华北',
          status: 'online',
          enabled: true,
          draining: false,
          labels: { '用途': '批处理' },
          runtimes: ['bash', 'python3'],
          agentVersion: '0.1.0',
          schedulingWeight: 100,
          cpuUsagePercent: 37.5,
          memoryTotalBytes: 17179869184,
          memoryAvailableBytes: 10737418240,
          diskTotalBytes: 107374182400,
          diskAvailableBytes: 75161927680,
          runningTasks: 1,
          lastSeenAt: '2026-08-28T04:00:00Z',
        }],
      }),
    }))
    const user = userEvent.setup()

    render(<ServersPage />)

    expect(await screen.findByText('京东云执行节点-1')).toBeVisible()
    expect(screen.getByText('京东云 · 华北')).toBeVisible()
    expect(screen.getByText('37.5%')).toBeVisible()
    expect(screen.getByText('用途：批处理')).toBeVisible()
    expect(screen.getByRole('button', { name: '排空' })).toBeVisible()
    expect(screen.getByRole('button', { name: '停用' })).toBeVisible()

    await user.click(screen.getByRole('button', { name: '查看京东云执行节点-1详情' }))
    expect(screen.getByRole('dialog', { name: '服务器详情' })).toBeVisible()
    expect(screen.getByText('调度权重')).toBeVisible()
    expect(screen.getByText('bash、python3')).toBeVisible()
  })
})
