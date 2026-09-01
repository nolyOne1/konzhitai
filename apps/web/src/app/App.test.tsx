import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { App } from './App'

describe('云令应用壳', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    window.history.pushState({}, '', '/')
  })

  it('显示全中文控制台品牌和主导航', () => {
    render(<App />)

    expect(screen.getByText('云令')).toBeInTheDocument()
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

  it('访问运维中心地址时显示账号安全页面', () => {
    window.history.pushState({}, '', '/operations')

    render(<App />)

    expect(screen.getByRole('heading', { level: 1, name: '运维中心' })).toBeVisible()
    expect(screen.getByRole('heading', { name: '账号安全' })).toBeVisible()
    window.history.pushState({}, '', '/')
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
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      if (String(input) === '/api/auth/logout') {
        return { ok: true, status: 204 } as Response
      }
      return {
        ok: false,
        status: 401,
        json: async () => ({ message: '请先登录' }),
      } as Response
    }))

    render(<App />)
    await user.click(screen.getByRole('button', { name: '退出登录' }))

    expect(await screen.findByRole('heading', { name: '登录云令' })).toBeVisible()
  })

  it('退出请求处理中禁用按钮并显示进度', async () => {
    const user = userEvent.setup()
    let finishLogout: ((response: Response) => void) | undefined
    const logoutResponse = new Promise<Response>((resolve) => { finishLogout = resolve })
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      if (String(input) === '/api/auth/logout') return logoutResponse
      return {
        ok: false,
        status: 401,
        json: async () => ({ message: '请先登录' }),
      } as Response
    }))

    render(<App />)
    await user.click(screen.getByRole('button', { name: '退出登录' }))

    expect(screen.getByRole('button', { name: '正在退出…' })).toBeDisabled()
    finishLogout?.({ ok: true, status: 204 } as Response)
    expect(await screen.findByRole('heading', { name: '登录云令' })).toBeVisible()
  })

  it('退出失败时保留控制台并显示中文错误', async () => {
    const user = userEvent.setup()
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      if (String(input) === '/api/auth/logout') {
        return {
          ok: false,
          status: 500,
          json: async () => ({ message: '退出登录失败，请稍后重试' }),
        } as Response
      }
      return {
        ok: false,
        status: 401,
        json: async () => ({ message: '请先登录' }),
      } as Response
    }))

    render(<App />)
    await user.click(screen.getByRole('button', { name: '退出登录' }))

    expect(await screen.findByRole('alert', { name: '退出登录失败' })).toHaveTextContent('退出登录失败，请稍后重试')
    expect(screen.getByRole('heading', { name: '运行总览' })).toBeVisible()
    expect(screen.getByRole('button', { name: '退出登录' })).toBeEnabled()
  })
})
