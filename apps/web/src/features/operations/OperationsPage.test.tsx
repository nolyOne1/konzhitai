import { render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'

import { OperationsPage } from './OperationsPage'

vi.mock('../../api/client', () => ({
  changePassword: vi.fn(),
  getFeishuNotificationConfig: vi.fn().mockResolvedValue({ configured: false, enabled: false, maskedDestination: '' }),
  getSession: vi.fn().mockResolvedValue({ id: 'admin-1', displayName: '管理员', email: 'admin@example.com', roles: ['admin'] }),
  getNotificationDelivery: vi.fn(),
  testFeishuNotification: vi.fn(),
  updateFeishuNotificationConfig: vi.fn(),
}))

describe('运维中心页面', () => {
  it('显示运行保障说明、飞书通知和账号安全面板', async () => {
    render(<OperationsPage />)

    expect(screen.getByRole('heading', { level: 1, name: '运维中心' })).toBeVisible()
    expect(screen.getByRole('heading', { name: '账号安全' })).toBeVisible()
    expect(await screen.findByRole('heading', { name: '飞书通知' })).toBeVisible()
    expect(screen.getByText('修改密码后，当前会话继续保留，其他设备需要重新登录。')).toBeVisible()
  })
})
