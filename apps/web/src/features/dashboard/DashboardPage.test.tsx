import { fireEvent, render, screen } from '@testing-library/react'
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

  it('显示真实任务和当前发布版本同步结果', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true, json: async () => ({
      onlineServers: 1, totalServers: 1, runningRuns: 1, queuedRuns: 0,
      todaySuccessRate: 100, servers: [], recentEvents: [],
      activeRuns: [{ id: 'run-1', taskName: '订单同步', scriptName: '同步脚本', serverName: '执行节点一', state: 'running', resultSummary: '已缓存脚本版本', queuedAt: '2026-09-23T08:00:00Z' }],
      scriptSync: { publishedScripts: 2, total: 3, ready: 1, pending: 0, downloading: 0, failed: 1, drifted: 1 },
    }) }))
    render(<DashboardPage />)
    expect(await screen.findByRole('link', { name: '订单同步' })).toHaveAttribute('href', '/runs/run-1')
    expect(screen.getByText(/执行节点一/)).toBeVisible()
    expect(screen.getByText('已缓存脚本版本')).toBeVisible()
    expect(screen.getByText('1 / 3')).toBeVisible()
    expect(screen.getByText('失败 1')).toBeVisible()
    expect(screen.getByText('漂移 1')).toBeVisible()
    expect(screen.queryByText('尚无已发布脚本')).not.toBeInTheDocument()
  })

  it('刷新失败保留上次数据，并允许手动恢复', async () => {
    const data = { onlineServers: 1, totalServers: 1, runningRuns: 7, queuedRuns: 0, todaySuccessRate: 100, servers: [], recentEvents: [], activeRuns: [], scriptSync: { publishedScripts: 0, total: 0, ready: 0, pending: 0, downloading: 0, failed: 0, drifted: 0 } }
    const fetch = vi.fn()
      .mockResolvedValueOnce({ ok: true, json: async () => data })
      .mockRejectedValueOnce(new Error('连接中断'))
      .mockResolvedValueOnce({ ok: true, json: async () => ({ ...data, runningRuns: 2 }) })
    vi.stubGlobal('fetch', fetch)
    render(<DashboardPage />)
    expect(await screen.findByText('7')).toBeVisible()
    fireEvent.click(screen.getByRole('button', { name: '刷新总览' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('保留上次成功数据')
    expect(screen.getByText('7')).toBeVisible()
    fireEvent.click(screen.getByRole('button', { name: '刷新总览' }))
    expect(await screen.findByText('2')).toBeVisible()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })
})
