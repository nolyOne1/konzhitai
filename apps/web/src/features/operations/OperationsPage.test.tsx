import { render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'

import { OperationsPage } from './OperationsPage'

vi.mock('../../api/client', () => ({ changePassword: vi.fn() }))

describe('运维中心页面', () => {
  it('显示运行保障说明和账号安全面板', () => {
    render(<OperationsPage />)

    expect(screen.getByRole('heading', { level: 1, name: '运维中心' })).toBeVisible()
    expect(screen.getByRole('heading', { name: '账号安全' })).toBeVisible()
    expect(screen.getByText('修改密码后，当前会话继续保留，其他设备需要重新登录。')).toBeVisible()
  })
})
