import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { MemoryRouter, Route, Routes } from 'react-router-dom'

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
    renderLoginPage()

    await user.type(screen.getByLabelText('邮箱'), 'ops@example.com')
    await user.type(screen.getByLabelText('密码'), 'bad')
    await user.click(screen.getByRole('button', { name: '登录' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('账号或密码错误')
    expect(fetchMock).toHaveBeenCalledWith('/api/auth/login', expect.objectContaining({
      method: 'POST',
      credentials: 'same-origin',
    }))
  })

  it.each([
    { mustChangePassword: true, destination: '必须修改密码' },
    { mustChangePassword: false, destination: '运行总览' },
  ])('登录成功后按临时密码标志进入$destination', async ({ mustChangePassword, destination }) => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({ must_change_password: mustChangePassword }),
    }))
    const user = userEvent.setup()
    renderLoginPage()

    await user.type(screen.getByLabelText('邮箱'), 'ops@example.com')
    await user.type(screen.getByLabelText('密码'), 'member-password')
    await user.click(screen.getByRole('button', { name: '登录' }))

    expect(await screen.findByRole('heading', { name: destination })).toBeVisible()
  })
})

function renderLoginPage() {
  render(
    <MemoryRouter initialEntries={['/login']}>
      <Routes>
        <Route path="/login" element={<LoginPage />} />
        <Route path="/password-required" element={<h1>必须修改密码</h1>} />
        <Route path="/" element={<h1>运行总览</h1>} />
      </Routes>
    </MemoryRouter>,
  )
}
