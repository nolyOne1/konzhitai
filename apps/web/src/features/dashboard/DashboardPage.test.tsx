import { render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { DashboardPage } from './DashboardPage'

describe('运行总览', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('把排队作为控制台运行规则展示', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({
        onlineServers: 3,
        totalServers: 4,
        runningRuns: 6,
        queuedRuns: 12,
        todaySuccessRate: 98.4,
        servers: [],
        recentEvents: [],
      }),
    }))

    render(<DashboardPage />)

    expect(await screen.findByText('排队任务')).toBeVisible()
    expect(screen.getByText('12')).toBeVisible()
    expect(screen.getByText('队列自动调度已开启')).toBeVisible()
    expect(screen.queryByText('排队队列')).not.toBeInTheDocument()
    expect(screen.getByRole('heading', { name: '服务器负载' })).toBeVisible()
    expect(screen.getByRole('heading', { name: '实时任务' })).toBeVisible()
    expect(screen.getByRole('heading', { name: '脚本同步' })).toBeVisible()
  })
})
