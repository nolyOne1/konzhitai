import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { MembersPage } from './MembersPage'

describe('团队与权限', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('管理员可通过键盘友好的对话框调整成员角色', async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input)
      if (path === '/api/auth/session') return response({ user: { user_id: 'admin-1', display_name: '管理员', roles: ['admin'] } })
      if (path.includes('/roles') && init?.method === 'PUT') return response({ id: 'user-2', email: 'ops@example.com', displayName: '值班运维', enabled: true, roles: ['operator'], createdAt: '2026-08-29T02:00:00Z' })
      return response({ members: [{ id: 'user-2', email: 'ops@example.com', displayName: '值班运维', enabled: true, roles: ['viewer'], createdAt: '2026-08-29T02:00:00Z' }] })
    })
    vi.stubGlobal('fetch', fetchMock)
    const user = userEvent.setup()
    render(<MembersPage />)

    expect(await screen.findByText('值班运维')).toBeVisible()
    await user.click(screen.getByRole('button', { name: '调整值班运维的角色' }))
    expect(screen.getByRole('dialog', { name: '调整成员角色' })).toBeVisible()
    await user.click(screen.getByRole('checkbox', { name: '运维人员' }))
    await user.click(screen.getByRole('checkbox', { name: '只读成员' }))
    await user.click(screen.getByRole('button', { name: '保存角色' }))

    expect(within(screen.getByRole('table')).getByText('运维人员')).toBeVisible()
    expect(fetchMock).toHaveBeenCalledWith('/api/members/user-2/roles', expect.objectContaining({ method: 'PUT' }))
  })
})

function response(body: unknown, status = 200) {
  return { ok: status >= 200 && status < 300, status, json: async () => body } as Response
}
