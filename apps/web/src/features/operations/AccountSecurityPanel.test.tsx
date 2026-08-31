import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { changePassword } from '../../api/client'
import { AccountSecurityPanel } from './AccountSecurityPanel'

vi.mock('../../api/client', () => ({ changePassword: vi.fn() }))

describe('账号安全面板', () => {
  beforeEach(() => {
    vi.mocked(changePassword).mockReset()
  })

  it('提供可访问的密码字段和自动填充语义', () => {
    render(<AccountSecurityPanel />)

    expect(screen.getByLabelText('当前密码')).toHaveAttribute('autocomplete', 'current-password')
    expect(screen.getByLabelText('新密码')).toHaveAttribute('autocomplete', 'new-password')
    expect(screen.getByLabelText('确认新密码')).toHaveAttribute('autocomplete', 'new-password')
  })

  it('拒绝少于十二位的新密码并聚焦错误摘要', async () => {
    const user = userEvent.setup()
    render(<AccountSecurityPanel />)

    await user.type(screen.getByLabelText('当前密码'), 'current-password')
    await user.type(screen.getByLabelText('新密码'), 'short')
    await user.type(screen.getByLabelText('确认新密码'), 'short')
    await user.click(screen.getByRole('button', { name: '更新密码' }))

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('新密码至少需要 12 位')
    expect(alert).toHaveFocus()
    expect(changePassword).not.toHaveBeenCalled()
  })

  it('拒绝两次输入不一致的新密码', async () => {
    const user = userEvent.setup()
    render(<AccountSecurityPanel />)

    await user.type(screen.getByLabelText('当前密码'), 'current-password')
    await user.type(screen.getByLabelText('新密码'), 'new-password-2026')
    await user.type(screen.getByLabelText('确认新密码'), 'different-password-2026')
    await user.click(screen.getByRole('button', { name: '更新密码' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('两次输入的新密码不一致')
    expect(changePassword).not.toHaveBeenCalled()
  })

  it('成功后清空密码并提示其他设备已退出', async () => {
    vi.mocked(changePassword).mockResolvedValue(undefined)
    const user = userEvent.setup()
    render(<AccountSecurityPanel />)

    const current = screen.getByLabelText('当前密码')
    const next = screen.getByLabelText('新密码')
    const confirmation = screen.getByLabelText('确认新密码')
    await user.type(current, 'current-password')
    await user.type(next, 'new-password-2026')
    await user.type(confirmation, 'new-password-2026')
    await user.click(screen.getByRole('button', { name: '更新密码' }))

    expect(await screen.findByRole('status')).toHaveTextContent('密码已更新，其他设备已退出')
    expect(changePassword).toHaveBeenCalledWith('current-password', 'new-password-2026')
    expect(current).toHaveValue('')
    expect(next).toHaveValue('')
    expect(confirmation).toHaveValue('')
    expect(document.body).not.toHaveTextContent('new-password-2026')
  })
})
