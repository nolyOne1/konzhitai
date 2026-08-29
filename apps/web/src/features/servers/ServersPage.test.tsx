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

  it('管理员可轮换并紧急吊销代理凭据', async () => {
    const server = {
      id: 'server-1', name: '京东云执行节点-1', cloudProvider: '京东云', region: '华北', status: 'online',
      enabled: true, draining: false, labels: {}, runtimes: ['bash'], agentVersion: '0.1.0', schedulingWeight: 100,
      cpuUsagePercent: 20, memoryTotalBytes: 8589934592, memoryAvailableBytes: 4294967296,
      diskTotalBytes: 107374182400, diskAvailableBytes: 75161927680, runningTasks: 0, lastSeenAt: '2026-08-29T03:00:00Z',
    }
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input)
      if (path === '/api/auth/session') return { ok: true, status: 200, json: async () => ({ user: { user_id: 'admin-1', display_name: '管理员', email: 'admin@example.com', roles: ['admin'] } }) } as Response
      if (path.endsWith('/credentials/rotate')) return { ok: true, status: 201, json: async () => ({ server_id: 'server-1', credential: 'shown-once-credential' }) } as Response
      if (path.endsWith('/credentials/revoke')) return { ok: true, status: 204, json: async () => ({}) } as Response
      return { ok: true, status: 200, json: async () => ({ servers: [server] }) } as Response
    })
    vi.stubGlobal('fetch', fetchMock)
    const user = userEvent.setup()
    render(<ServersPage />)

    await user.click(await screen.findByRole('button', { name: '查看京东云执行节点-1详情' }))
    await user.click(await screen.findByRole('button', { name: '轮换代理凭据' }))
    expect(await screen.findByText('shown-once-credential')).toBeVisible()
    await user.click(screen.getByRole('button', { name: '紧急吊销全部凭据' }))
    await user.click(screen.getByRole('button', { name: '确认紧急吊销' }))
    expect(await screen.findByText('代理凭据已全部吊销，节点连接已断开。')).toBeVisible()
  })
})
