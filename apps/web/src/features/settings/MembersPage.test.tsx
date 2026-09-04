import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { MembersPage } from './MembersPage'

describe('团队与权限', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('管理员创建成员后只显示一次临时密码并可复制', async () => {
    const user = userEvent.setup()
    const writeText = vi.fn().mockResolvedValue(undefined)
    vi.stubGlobal('navigator', { ...navigator, clipboard: { writeText } })
    stubMemberApi({ temporaryPassword: 'temporary-password' })
    render(<MembersPage />)

    await user.click(await screen.findByRole('button', { name: '创建成员' }))
    await user.type(screen.getByLabelText('姓名'), '值班运维')
    await user.type(screen.getByLabelText('邮箱'), 'ops@example.com')
    await user.click(screen.getByRole('checkbox', { name: '运维人员' }))
    await user.click(screen.getByRole('button', { name: '确认创建' }))

    const passwordDialog = await screen.findByRole('dialog', { name: '一次性临时密码' })
    expect(passwordDialog).toHaveTextContent('temporary-password')
    await user.click(within(passwordDialog).getByRole('button', { name: '复制临时密码' }))
    expect(writeText).toHaveBeenCalledWith('temporary-password')
    await user.click(within(passwordDialog).getByRole('button', { name: '我已保存，关闭' }))
    expect(screen.queryByText('temporary-password')).not.toBeInTheDocument()
  })

  it('按状态筛选成员并用 aria-pressed 标识当前筛选', async () => {
    const user = userEvent.setup()
    const fetchMock = stubMemberApi({ temporaryPassword: 'temporary-password' })
    render(<MembersPage />)

    const removed = await screen.findByRole('button', { name: '已移除' })
    expect(screen.getByRole('button', { name: '全部' })).toHaveAttribute('aria-pressed', 'true')
    await user.click(removed)

    expect(removed).toHaveAttribute('aria-pressed', 'true')
    expect(fetchMock).toHaveBeenCalledWith('/api/members?status=removed', expect.anything())
  })

  it('停用、移除和重置密码会说明会话立即失效', async () => {
    const user = userEvent.setup()
    stubMemberApi({ temporaryPassword: 'reset-password', members: [member()] })
    render(<MembersPage />)

    await openActions(user)
    await user.click(screen.getByRole('menuitem', { name: '停用成员' }))
    expect(await screen.findByRole('dialog', { name: '确认停用成员' })).toHaveTextContent('所有现有会话会立即失效')
    await user.click(screen.getByRole('button', { name: '确认停用' }))

    await openActions(user)
    await user.click(screen.getByRole('menuitem', { name: '重置密码' }))
    expect(await screen.findByRole('dialog', { name: '确认重置密码' })).toHaveTextContent('所有现有会话会立即失效')
    await user.click(screen.getByRole('button', { name: '确认重置密码' }))
    await user.click(screen.getByRole('button', { name: '我已保存，关闭' }))

    await openActions(user)
    await user.click(screen.getByRole('menuitem', { name: '移除成员' }))
    expect(await screen.findByRole('dialog', { name: '确认移除成员' })).toHaveTextContent('所有现有会话会立即失效')
  })

  it('成员操作失败时在确认窗口中显示服务错误', async () => {
    const user = userEvent.setup()
    stubMemberApi({ temporaryPassword: 'temporary-password', members: [member()], disableError: true })
    render(<MembersPage />)

    await openActions(user)
    await user.click(screen.getByRole('menuitem', { name: '停用成员' }))
    await user.click(screen.getByRole('button', { name: '确认停用' }))
    expect(await within(screen.getByRole('dialog', { name: '确认停用成员' })).findByRole('alert')).toHaveTextContent('禁止操作')
  })

  it('恢复成员后保持停用状态，且不能操作当前管理员本人', async () => {
    const user = userEvent.setup()
    const removed = member({ id: 'user-2', enabled: false, removedAt: '2026-09-04T01:00:00Z' })
    stubMemberApi({ temporaryPassword: 'temporary-password', members: [member({ id: 'admin-1', displayName: '管理员' }), removed] })
    render(<MembersPage />)

    await user.click(await screen.findByRole('button', { name: '已移除' }))
    await user.click(await screen.findByRole('button', { name: '更多值班运维操作' }))
    await user.click(screen.getByRole('menuitem', { name: '恢复成员' }))
    expect(await screen.findByRole('dialog', { name: '确认恢复成员' })).toHaveTextContent('账号会保持停用状态')
    await user.click(screen.getByRole('button', { name: '确认恢复' }))

    expect(await screen.findByText('已停用', { selector: '.status-badge' })).toBeVisible()
    expect(screen.queryByRole('button', { name: '更多管理员操作' })).not.toBeInTheDocument()
  })

  it('只读成员看不到创建和任何成员变更控件', async () => {
    stubMemberApi({ temporaryPassword: 'temporary-password', roles: ['viewer'], members: [member()] })
    render(<MembersPage />)

    await screen.findByText('值班运维')
    expect(screen.queryByRole('button', { name: '创建成员' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '更多值班运维操作' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '调整值班运维的角色' })).not.toBeInTheDocument()
  })

  it('管理员可通过键盘友好的对话框调整成员角色', async () => {
    const fetchMock = stubMemberApi({ temporaryPassword: 'temporary-password', members: [member({ roles: ['viewer'] })] })
    const user = userEvent.setup()
    render(<MembersPage />)

    expect(await screen.findByText('值班运维')).toBeVisible()
    await user.click(screen.getByRole('button', { name: '更多值班运维操作' }))
    await user.click(screen.getByRole('menuitem', { name: '调整角色' }))
    expect(screen.getByRole('dialog', { name: '调整成员角色' })).toBeVisible()
    await user.click(screen.getByRole('checkbox', { name: '运维人员' }))
    await user.click(screen.getByRole('checkbox', { name: '只读成员' }))
    await user.click(screen.getByRole('button', { name: '保存角色' }))

    expect(within(screen.getByRole('table')).getByText('运维人员')).toBeVisible()
    expect(fetchMock).toHaveBeenCalledWith('/api/members/user-2/roles', expect.objectContaining({ method: 'PUT' }))
  })
})

async function openActions(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await screen.findByRole('button', { name: '更多值班运维操作' }))
}

type TestMember = {
  id: string
  email: string
  displayName: string
  enabled: boolean
  mustChangePassword: boolean
  removedAt: string | null
  roles: Array<'admin' | 'operator' | 'developer' | 'viewer'>
  createdAt: string
}

function member(overrides: Partial<TestMember> = {}): TestMember {
  return {
    id: 'user-2', email: 'ops@example.com', displayName: '值班运维', enabled: true,
    mustChangePassword: false, removedAt: null, roles: ['operator'], createdAt: '2026-09-04T00:00:00Z',
    ...overrides,
  }
}

function stubMemberApi(options: { temporaryPassword: string; roles?: TestMember['roles']; members?: TestMember[]; disableError?: boolean }) {
  const members = options.members ?? []
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input)
    const target = members.find((item) => path.includes(item.id)) ?? member()
    if (path === '/api/auth/session') return response({ user: {
      user_id: 'admin-1', email: 'admin@example.com', display_name: '管理员',
      roles: options.roles ?? ['admin'], must_change_password: false,
    } })
    if (path === '/api/members' && init?.method === 'POST') {
      return response({ member: member({ mustChangePassword: true }), temporaryPassword: options.temporaryPassword }, 201)
    }
    if (path.startsWith('/api/members?')) {
      const status = new URL(path, 'http://localhost').searchParams.get('status')
      return response({ members: status === 'removed' ? members.filter((item) => item.removedAt) : members.filter((item) => !item.removedAt) })
    }
    if (path.includes('/roles') && init?.method === 'PUT') return response(member({ roles: ['operator'] }))
    if (path.endsWith('/disable')) return options.disableError ? response({ message: '禁止操作' }, 403) : response({ ...target, enabled: false })
    if (path.endsWith('/enable')) return response({ ...target, enabled: true })
    if (path.endsWith('/restore')) return response({ ...target, enabled: false, removedAt: null })
    if (path.endsWith('/password/reset')) return response({ member: target, temporaryPassword: options.temporaryPassword })
    if (init?.method === 'DELETE') return response({})
    return response(target)
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

function response(body: unknown, status = 200) {
  return { ok: status >= 200 && status < 300, status, json: async () => body } as Response
}
