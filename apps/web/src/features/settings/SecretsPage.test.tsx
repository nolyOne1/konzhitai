import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { SecretsPage } from './SecretsPage'

describe('参数与密钥', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('管理员可创建敏感参数，页面不会再次显示明文', async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input)
      if (path === '/api/auth/session') return response({ user: { user_id: 'admin-1', display_name: '管理员', roles: ['admin'] } })
      if (path === '/api/secrets' && init?.method === 'POST') return response({ id: 'secret-2', name: '新令牌', createdBy: 'admin-1', createdAt: '2026-08-29T03:00:00Z', updatedAt: '2026-08-29T03:00:00Z' }, 201)
      return response({ secrets: [{ id: 'secret-1', name: '生产数据库密码', createdBy: 'admin-1', createdAt: '2026-08-29T02:00:00Z', updatedAt: '2026-08-29T02:00:00Z' }] })
    })
    vi.stubGlobal('fetch', fetchMock)
    const user = userEvent.setup()
    render(<SecretsPage />)

    expect(await screen.findByText('生产数据库密码')).toBeVisible()
    const open = screen.getByRole('button', { name: '创建敏感参数' })
    await user.click(open)
    expect(screen.getByRole('dialog', { name: '创建敏感参数' })).toBeVisible()
    expect(screen.getByLabelText('名称')).toHaveFocus()
    await user.type(screen.getByLabelText('名称'), '新令牌')
    await user.type(screen.getByLabelText('敏感值'), 'never-show-again')
    await user.click(screen.getByRole('button', { name: '加密保存' }))

    expect(await screen.findByText('新令牌')).toBeVisible()
    expect(screen.queryByDisplayValue('never-show-again')).not.toBeInTheDocument()
    expect(open).toHaveFocus()
    expect(fetchMock).toHaveBeenCalledWith('/api/secrets', expect.objectContaining({ method: 'POST' }))
  })

  it('只读成员看不到创建按钮', async () => {
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => String(input) === '/api/auth/session'
      ? response({ user: { user_id: 'viewer-1', display_name: '观察员', roles: ['viewer'] } })
      : response({ secrets: [] })))
    render(<SecretsPage />)
    await screen.findByText('尚未创建敏感参数')
    expect(screen.queryByRole('button', { name: '创建敏感参数' })).not.toBeInTheDocument()
  })
})

function response(body: unknown, status = 200) {
  return { ok: status >= 200 && status < 300, status, json: async () => body } as Response
}
