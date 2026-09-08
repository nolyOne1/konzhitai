import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { changePassword, getSession, logout } from '../api/client'
import { App } from './App'

vi.mock('../api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api/client')>()
  return { ...actual, changePassword: vi.fn(), getSession: vi.fn(), logout: vi.fn() }
})

describe('云令应用壳', () => {
  beforeEach(() => {
    vi.mocked(changePassword).mockReset()
    vi.mocked(getSession).mockReset()
    vi.mocked(logout).mockReset()
    vi.mocked(getSession).mockResolvedValue({
      id: 'admin-1', email: 'admin@example.com', displayName: '管理员',
      roles: ['admin'], mustChangePassword: false,
    })
    vi.mocked(logout).mockResolvedValue(undefined)
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    window.history.pushState({}, '', '/')
  })

  it('显示全中文控制台品牌和主导航', async () => {
    render(<App />)

    expect(await screen.findByText('云令')).toBeInTheDocument()
    expect(screen.getByText('脚本调度中心')).toBeInTheDocument()
    expect(screen.getByRole('navigation', { name: '主导航' })).toBeVisible()
    expect(screen.getByRole('link', { name: '跳到主要内容' })).toHaveAttribute('href', '#main-content')
    expect(screen.getByRole('link', { name: '运行总览' })).toBeVisible()
    expect(screen.getByRole('link', { name: '脚本中心' })).toBeVisible()
    expect(screen.getByRole('link', { name: '任务调度' })).toBeVisible()
    expect(screen.getByRole('link', { name: '执行记录' })).toBeVisible()
    expect(screen.getByRole('link', { name: '服务器' })).toBeVisible()
    expect(screen.getByRole('link', { name: '运维中心' })).toBeVisible()
  })

  it('访问运维中心地址时显示账号安全页面', async () => {
    window.history.pushState({}, '', '/operations')

    render(<App />)

    expect(await screen.findByRole('heading', { level: 1, name: '运维中心' })).toBeVisible()
    expect(screen.getByRole('heading', { name: '账号安全' })).toBeVisible()
    window.history.pushState({}, '', '/')
  })

  it('访问脚本同步地址时显示同步总览而不是占位页', async () => {
    window.history.pushState({}, '', '/sync')
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      if (String(input) === '/api/scripts') {
        return { ok: true, json: async () => ({ scripts: [] }) } as Response
      }
      return { ok: false, status: 404, json: async () => ({ message: '未找到资源' }) } as Response
    }))

    render(<App />)

    expect(await screen.findByRole('heading', { level: 1, name: '脚本同步' })).toBeVisible()
    expect(screen.queryByRole('heading', { name: '控制台模块' })).not.toBeInTheDocument()
    window.history.pushState({}, '', '/')
  })

  it('访问服务器升级地址时显示代理升级工作台', async () => {
    window.history.pushState({}, '', '/servers/upgrades')
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const path = String(input)
      if (path === '/api/agent-releases') return { ok: true, status: 200, json: async () => ({ releases: [] }) } as Response
      if (path === '/api/servers') return { ok: true, status: 200, json: async () => ({ servers: [] }) } as Response
      if (path === '/api/agent-upgrades') return { ok: true, status: 200, json: async () => ({ plans: [] }) } as Response
      return { ok: false, status: 404, json: async () => ({ message: '未找到资源' }) } as Response
    }))

    render(<App />)

    expect(await screen.findByRole('heading', { level: 1, name: '代理升级' })).toBeVisible()
  })

  it('访问登录地址时显示中文登录页', () => {
    window.history.pushState({}, '', '/login')

    render(<App />)

    expect(screen.getByRole('heading', { name: '登录云令' })).toBeVisible()
    expect(screen.getByLabelText('邮箱')).toBeVisible()
    window.history.pushState({}, '', '/')
  })

  it('退出当前会话后返回登录页', async () => {
    const user = userEvent.setup()

    render(<App />)
    await user.click(await screen.findByRole('button', { name: '退出登录' }))

    expect(await screen.findByRole('heading', { name: '登录云令' })).toBeVisible()
  })

  it('退出请求处理中禁用按钮并显示进度', async () => {
    const user = userEvent.setup()
    let finishLogout: (() => void) | undefined
    vi.mocked(logout).mockImplementation(() => new Promise<void>((resolve) => { finishLogout = resolve }))

    render(<App />)
    await user.click(await screen.findByRole('button', { name: '退出登录' }))

    expect(screen.getByRole('button', { name: '正在退出…' })).toBeDisabled()
    finishLogout?.()
    expect(await screen.findByRole('heading', { name: '登录云令' })).toBeVisible()
  })

  it('退出失败时保留控制台并显示中文错误', async () => {
    const user = userEvent.setup()
    vi.mocked(logout).mockRejectedValue(new Error('退出登录失败，请稍后重试'))

    render(<App />)
    await user.click(await screen.findByRole('button', { name: '退出登录' }))

    expect(await screen.findByRole('alert', { name: '退出登录失败' })).toHaveTextContent('退出登录失败，请稍后重试')
    expect(screen.getByRole('heading', { name: '运行总览' })).toBeVisible()
    expect(screen.getByRole('button', { name: '退出登录' })).toBeEnabled()
  })

  it('临时密码用户完成改密后进入运行总览', async () => {
    vi.mocked(getSession).mockResolvedValue({
      id: 'user-2', email: 'ops@example.com', displayName: '值班运维',
      roles: ['operator'], mustChangePassword: true,
    })
    vi.mocked(changePassword).mockResolvedValue(undefined)
    window.history.pushState({}, '', '/')
    render(<App />)
    expect(await screen.findByRole('heading', { name: '首次登录，请修改密码' })).toBeVisible()
    await fillAndSubmitPasswordForm(userEvent.setup())
    expect(await screen.findByRole('heading', { name: '运行总览' })).toBeVisible()
  })

  async function fillAndSubmitPasswordForm(user: ReturnType<typeof userEvent.setup>) {
    await user.type(screen.getByLabelText('当前密码'), 'temporary-password')
    await user.type(screen.getByLabelText('新密码'), 'member-password-2026')
    await user.type(screen.getByLabelText('确认新密码'), 'member-password-2026')
    await user.click(screen.getByRole('button', { name: '更新密码' }))
  }

  it('普通会话不会看到必改密页面', async () => {
    window.history.pushState({}, '', '/')

    render(<App />)

    expect(await screen.findByRole('heading', { name: '运行总览' })).toBeVisible()
    expect(screen.queryByRole('heading', { name: '首次登录，请修改密码' })).not.toBeInTheDocument()
  })

  it('普通会话手动访问必改密地址时返回运行总览', async () => {
    window.history.pushState({}, '', '/password-required')

    render(<App />)

    expect(await screen.findByRole('heading', { name: '运行总览' })).toBeVisible()
    expect(window.location.pathname).toBe('/')
  })

  it('检查会话期间显示中文进度状态', () => {
    vi.mocked(getSession).mockImplementation(() => new Promise(() => undefined))

    render(<App />)

    expect(screen.getByRole('status')).toHaveTextContent('正在检查登录状态…')
  })

  it('未登录用户访问控制台时返回登录页', async () => {
    vi.mocked(getSession).mockRejectedValue(new Error('请先登录'))

    render(<App />)

    expect(await screen.findByRole('heading', { name: '登录云令' })).toBeVisible()
    expect(window.location.pathname).toBe('/login')
  })
})
