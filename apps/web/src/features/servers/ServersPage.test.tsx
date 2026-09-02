import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { StrictMode } from 'react'
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

  it('管理员可创建一次性注册令牌并复制新服务器安装命令', async () => {
    let enrollmentInput: unknown
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input)
      if (path === '/api/auth/session') {
        return { ok: true, status: 200, json: async () => ({ user: { user_id: 'admin-1', display_name: '管理员', email: 'admin@example.com', roles: ['admin'] } }) } as Response
      }
      if (path === '/api/servers/enrollment-tokens') {
        enrollmentInput = JSON.parse(String(init?.body))
        return { ok: true, status: 201, json: async () => ({ id: 'token-1', token: 'enroll-once-token', expires_at: '2026-09-02T04:10:00Z' }) } as Response
      }
      return { ok: true, status: 200, json: async () => ({ servers: [] }) } as Response
    }))
    const user = userEvent.setup()
    render(<StrictMode><ServersPage /></StrictMode>)

    const openButton = screen.getByRole('button', { name: '接入服务器' })
    await waitFor(() => expect(openButton).toBeEnabled())
    await user.click(openButton)
    const dialog = screen.getByRole('dialog', { name: '接入新服务器' })
    await user.type(within(dialog).getByLabelText('服务器名称'), '阿里云执行节点-1')
    await user.selectOptions(within(dialog).getByLabelText('云厂商'), '阿里云')
    await user.type(within(dialog).getByLabelText('地域'), '华东 1')
    await user.type(within(dialog).getByLabelText(/服务器标签/), '用途=批处理, 环境=生产')
    await user.click(within(dialog).getByRole('button', { name: '创建一次性令牌' }))

    expect(await within(dialog).findByText('enroll-once-token')).toBeVisible()
    expect(within(dialog).getByLabelText('代理安装命令')).toHaveTextContent(`YUNLING_CONTROL_URL='${window.location.origin}'`)
    expect(within(dialog).getByLabelText('代理安装命令').textContent?.trim()).toMatch(/^YUNLING_CONTROL_URL=.* bash -s <<'YUNLING_INSTALL'/)
    expect(within(dialog).getByLabelText('代理安装命令')).toHaveTextContent('set -euo pipefail')
    expect(within(dialog).getByLabelText('代理安装命令')).toHaveTextContent('trap')
    expect(within(dialog).getByLabelText('代理安装命令')).toHaveTextContent('read -rsp')
    expect(within(dialog).getByLabelText('代理安装命令')).not.toHaveTextContent('enroll-once-token')
    expect(enrollmentInput).toEqual({ name: '阿里云执行节点-1', cloud_provider: '阿里云', region: '华东 1', labels: { '用途': '批处理', '环境': '生产' } })
    expect(within(dialog).getByRole('status')).toHaveFocus()

    await user.click(within(dialog).getByRole('button', { name: '复制注册令牌' }))
    expect(await within(dialog).findByText('注册令牌已复制')).toBeVisible()
    expect(await navigator.clipboard.readText()).toBe('enroll-once-token')
    await user.click(within(dialog).getByRole('button', { name: '复制安装命令' }))
    expect(await within(dialog).findByText('安装命令已复制')).toBeVisible()
    expect(await navigator.clipboard.readText()).toContain('read -rsp')
    expect(await navigator.clipboard.readText()).not.toContain('enroll-once-token')

    await user.click(within(dialog).getByRole('button', { name: '关闭接入向导' }))
    expect(screen.queryByText('enroll-once-token')).not.toBeInTheDocument()
  })

  it('非管理员不能打开服务器接入向导', async () => {
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      if (String(input) === '/api/auth/session') {
        return { ok: true, status: 200, json: async () => ({ user: { user_id: 'viewer-1', display_name: '只读成员', email: 'viewer@example.com', roles: ['viewer'] } }) } as Response
      }
      return { ok: true, status: 200, json: async () => ({ servers: [] }) } as Response
    }))
    render(<ServersPage />)

    const openButton = screen.getByRole('button', { name: '接入服务器' })
    await waitFor(() => expect(screen.getByText('仅管理员可接入新服务器')).toBeVisible())
    expect(openButton).toBeDisabled()
  })

  it('注册令牌创建失败时在向导内保留表单并显示原因', async () => {
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const path = String(input)
      if (path === '/api/auth/session') {
        return { ok: true, status: 200, json: async () => ({ user: { user_id: 'admin-1', display_name: '管理员', email: 'admin@example.com', roles: ['admin'] } }) } as Response
      }
      if (path === '/api/servers/enrollment-tokens') {
        return { ok: false, status: 500, json: async () => ({ message: '注册服务暂时不可用' }) } as Response
      }
      return { ok: true, status: 200, json: async () => ({ servers: [] }) } as Response
    }))
    const user = userEvent.setup()
    render(<ServersPage />)

    const openButton = screen.getByRole('button', { name: '接入服务器' })
    await waitFor(() => expect(openButton).toBeEnabled())
    await user.click(openButton)
    const dialog = screen.getByRole('dialog', { name: '接入新服务器' })
    await user.type(within(dialog).getByLabelText('服务器名称'), '测试节点')
    await user.selectOptions(within(dialog).getByLabelText('云厂商'), '京东云')
    await user.click(within(dialog).getByRole('button', { name: '创建一次性令牌' }))

    expect(await within(dialog).findByRole('alert')).toHaveTextContent('注册服务暂时不可用')
    expect(within(dialog).getByLabelText('服务器名称')).toHaveValue('测试节点')
  })

  it('网络异常时使用固定中文错误提示', async () => {
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const path = String(input)
      if (path === '/api/auth/session') {
        return { ok: true, status: 200, json: async () => ({ user: { user_id: 'admin-1', display_name: '管理员', email: 'admin@example.com', roles: ['admin'] } }) } as Response
      }
      if (path === '/api/servers/enrollment-tokens') throw new TypeError('Failed to fetch')
      return { ok: true, status: 200, json: async () => ({ servers: [] }) } as Response
    }))
    const user = userEvent.setup()
    render(<ServersPage />)

    const openButton = screen.getByRole('button', { name: '接入服务器' })
    await waitFor(() => expect(openButton).toBeEnabled())
    await user.click(openButton)
    const dialog = screen.getByRole('dialog', { name: '接入新服务器' })
    await user.type(within(dialog).getByLabelText('服务器名称'), '网络异常节点')
    await user.selectOptions(within(dialog).getByLabelText('云厂商'), '自建服务器')
    await user.click(within(dialog).getByRole('button', { name: '创建一次性令牌' }))

    const alert = await within(dialog).findByRole('alert')
    expect(alert).toHaveTextContent('创建服务器注册令牌失败，请检查网络后重试。')
    expect(alert).not.toHaveTextContent('Failed to fetch')
  })

  it('剪贴板拒绝复制时显示中文警报', async () => {
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const path = String(input)
      if (path === '/api/auth/session') {
        return { ok: true, status: 200, json: async () => ({ user: { user_id: 'admin-1', display_name: '管理员', email: 'admin@example.com', roles: ['admin'] } }) } as Response
      }
      if (path === '/api/servers/enrollment-tokens') {
        return { ok: true, status: 201, json: async () => ({ id: 'token-copy', token: 'copy-token', expires_at: '2026-09-02T04:10:00Z' }) } as Response
      }
      return { ok: true, status: 200, json: async () => ({ servers: [] }) } as Response
    }))
    const user = userEvent.setup()
    render(<ServersPage />)

    const openButton = screen.getByRole('button', { name: '接入服务器' })
    await waitFor(() => expect(openButton).toBeEnabled())
    await user.click(openButton)
    const dialog = screen.getByRole('dialog', { name: '接入新服务器' })
    await user.type(within(dialog).getByLabelText('服务器名称'), '复制测试节点')
    await user.selectOptions(within(dialog).getByLabelText('云厂商'), '自建服务器')
    await user.click(within(dialog).getByRole('button', { name: '创建一次性令牌' }))
    expect(await within(dialog).findByText('copy-token')).toBeVisible()

    vi.spyOn(navigator.clipboard, 'writeText').mockRejectedValueOnce(new Error('NotAllowedError'))
    await user.click(within(dialog).getByRole('button', { name: '复制注册令牌' }))

    const alert = await within(dialog).findByRole('alert')
    expect(alert).toHaveTextContent('复制失败，请手动选择内容。')
    expect(alert).not.toHaveTextContent('NotAllowedError')
  })

  it('签发请求完成前阻止关闭向导，避免产生无法查看的孤儿令牌', async () => {
    let resolveEnrollment!: (response: Response) => void
    const enrollmentResponse = new Promise<Response>((resolve) => { resolveEnrollment = resolve })
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const path = String(input)
      if (path === '/api/auth/session') {
        return { ok: true, status: 200, json: async () => ({ user: { user_id: 'admin-1', display_name: '管理员', email: 'admin@example.com', roles: ['admin'] } }) } as Response
      }
      if (path === '/api/servers/enrollment-tokens') return enrollmentResponse
      return { ok: true, status: 200, json: async () => ({ servers: [] }) } as Response
    }))
    const user = userEvent.setup()
    render(<ServersPage />)

    const openButton = screen.getByRole('button', { name: '接入服务器' })
    await waitFor(() => expect(openButton).toBeEnabled())
    await user.click(openButton)
    const dialog = screen.getByRole('dialog', { name: '接入新服务器' })
    await user.type(within(dialog).getByLabelText('服务器名称'), '慢响应节点')
    await user.selectOptions(within(dialog).getByLabelText('云厂商'), '腾讯云')
    await user.click(within(dialog).getByRole('button', { name: '创建一次性令牌' }))

    expect(within(dialog).getByRole('button', { name: '正在创建…' })).toBeDisabled()
    expect(within(dialog).getByRole('button', { name: '关闭接入向导' })).toBeDisabled()
    await user.keyboard('{Escape}')
    expect(screen.getByRole('dialog', { name: '接入新服务器' })).toBeVisible()

    resolveEnrollment({ ok: true, status: 201, json: async () => ({ id: 'token-2', token: 'slow-token', expires_at: '2026-09-02T04:10:00Z' }) } as Response)
    expect(await within(dialog).findByText('slow-token')).toBeVisible()
  })

  it('权限信息读取失败时允许管理员重试而不是误报为非管理员', async () => {
    let sessionAttempts = 0
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      if (String(input) === '/api/auth/session') {
        sessionAttempts += 1
        if (sessionAttempts === 1) return { ok: false, status: 503, json: async () => ({ message: '会话服务暂时不可用' }) } as Response
        return { ok: true, status: 200, json: async () => ({ user: { user_id: 'admin-1', display_name: '管理员', email: 'admin@example.com', roles: ['admin'] } }) } as Response
      }
      return { ok: true, status: 200, json: async () => ({ servers: [] }) } as Response
    }))
    const user = userEvent.setup()
    render(<ServersPage />)

    expect(await screen.findByRole('alert')).toHaveTextContent('权限信息加载失败，请重试')
    expect(screen.queryByText('仅管理员可接入新服务器')).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '重试权限检查' }))
    await waitFor(() => expect(screen.getByRole('button', { name: '接入服务器' })).toBeEnabled())
    expect(sessionAttempts).toBe(2)
  })

  it('拒绝会改变调度含义的错误标签格式', async () => {
    let enrollmentRequests = 0
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const path = String(input)
      if (path === '/api/auth/session') {
        return { ok: true, status: 200, json: async () => ({ user: { user_id: 'admin-1', display_name: '管理员', email: 'admin@example.com', roles: ['admin'] } }) } as Response
      }
      if (path === '/api/servers/enrollment-tokens') enrollmentRequests += 1
      return { ok: true, status: 200, json: async () => path === '/api/servers/enrollment-tokens' ? ({ id: 'token-3', token: 'unexpected-token', expires_at: '2026-09-02T04:10:00Z' }) : ({ servers: [] }) } as Response
    }))
    const user = userEvent.setup()
    render(<ServersPage />)

    const openButton = screen.getByRole('button', { name: '接入服务器' })
    await waitFor(() => expect(openButton).toBeEnabled())
    await user.click(openButton)
    const dialog = screen.getByRole('dialog', { name: '接入新服务器' })
    await user.type(within(dialog).getByLabelText('服务器名称'), '标签测试节点')
    await user.selectOptions(within(dialog).getByLabelText('云厂商'), '自建服务器')
    await user.type(within(dialog).getByLabelText(/服务器标签/), '用途批处理，环境=生产')
    await user.click(within(dialog).getByRole('button', { name: '创建一次性令牌' }))

    expect(await within(dialog).findByRole('alert')).toHaveTextContent('标签格式不正确')
    expect(enrollmentRequests).toBe(0)
  })
})
