import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { LoginPage } from './LoginPage'

describe('中文登录页', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('登录失败时在表单旁显示中文错误', async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: false,
      json: async () => ({ message: '账号或密码错误' }),
    })
    vi.stubGlobal('fetch', fetchMock)
    const user = userEvent.setup()
    render(<LoginPage />)

    await user.type(screen.getByLabelText('邮箱'), 'ops@example.com')
    await user.type(screen.getByLabelText('密码'), 'bad')
    await user.click(screen.getByRole('button', { name: '登录' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('账号或密码错误')
    expect(fetchMock).toHaveBeenCalledWith('/api/auth/login', expect.objectContaining({
      method: 'POST',
      credentials: 'same-origin',
    }))
  })
})
