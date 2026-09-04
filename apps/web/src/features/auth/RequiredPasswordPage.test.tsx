import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { changePassword } from '../../api/client'
import { RequiredPasswordPage } from './RequiredPasswordPage'

vi.mock('../../api/client', () => ({ changePassword: vi.fn() }))

describe('首次登录强制改密页', () => {
  beforeEach(() => {
    vi.mocked(changePassword).mockReset()
  })

  it('完成密码更新后通知会话门禁重新检查身份', async () => {
    vi.mocked(changePassword).mockResolvedValue(undefined)
    const onChanged = vi.fn()
    const user = userEvent.setup()
    render(<RequiredPasswordPage onChanged={onChanged} />)

    expect(screen.getByRole('heading', { name: '首次登录，请修改密码' })).toBeVisible()
    await user.type(screen.getByLabelText('当前密码'), 'temporary-password')
    await user.type(screen.getByLabelText('新密码'), 'member-password-2026')
    await user.type(screen.getByLabelText('确认新密码'), 'member-password-2026')
    await user.click(screen.getByRole('button', { name: '更新密码' }))

    expect(onChanged).toHaveBeenCalledOnce()
  })
})
