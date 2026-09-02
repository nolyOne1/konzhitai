import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import {
  getFeishuNotificationConfig,
  getNotificationDelivery,
  getSession,
  testFeishuNotification,
  updateFeishuNotificationConfig,
} from '../../api/client'
import { NotificationSettingsPanel } from './NotificationSettingsPanel'

vi.mock('../../api/client', () => ({
  getFeishuNotificationConfig: vi.fn(),
  getNotificationDelivery: vi.fn(),
  getSession: vi.fn(),
  testFeishuNotification: vi.fn(),
  updateFeishuNotificationConfig: vi.fn(),
}))

const webhook = 'https://open.feishu.cn/open-apis/bot/v2/hook/01234567-89ab-cdef-0123-456789abcdef'

describe('飞书通知设置面板', () => {
  beforeEach(() => {
    vi.mocked(getSession).mockResolvedValue({ id: 'admin-1', displayName: '管理员', email: 'admin@example.com', roles: ['admin'] })
    vi.mocked(getFeishuNotificationConfig).mockResolvedValue({ configured: false, enabled: false, maskedDestination: '' })
    vi.mocked(updateFeishuNotificationConfig).mockResolvedValue({ configured: true, enabled: true, maskedDestination: '飞书机器人 …cdef' })
  })

  it('保存后清空秘密输入且只显示脱敏目标', async () => {
    const user = userEvent.setup()
    render(<NotificationSettingsPanel pollIntervalMs={1} />)

    await user.type(await screen.findByLabelText('飞书 Webhook'), webhook)
    await user.type(screen.getByLabelText('签名密钥'), 'signing-secret')
    await user.click(screen.getByLabelText('启用飞书通知'))
    await user.click(screen.getByRole('button', { name: '保存通知设置' }))

    expect(await screen.findByText('飞书机器人 …cdef')).toBeVisible()
    expect(screen.getByLabelText('飞书 Webhook')).toHaveValue('')
    expect(screen.getByLabelText('签名密钥')).toHaveValue('')
    expect(document.body).not.toHaveTextContent(webhook)
    expect(document.body).not.toHaveTextContent('signing-secret')
  })

  it('viewer 只能查看脱敏配置', async () => {
    vi.mocked(getSession).mockResolvedValue({ id: 'viewer-1', displayName: '观察员', email: 'viewer@example.com', roles: ['viewer'] })
    vi.mocked(getFeishuNotificationConfig).mockResolvedValue({ configured: true, enabled: true, maskedDestination: '飞书机器人 …cdef' })
    render(<NotificationSettingsPanel pollIntervalMs={1} />)

    expect(await screen.findByText('飞书机器人 …cdef')).toBeVisible()
    expect(screen.getByText('当前账号仅可查看通知状态。')).toBeVisible()
    expect(screen.queryByLabelText('飞书 Webhook')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '保存通知设置' })).not.toBeInTheDocument()
  })

  it('测试消息从 pending 轮询到 sent', async () => {
    vi.mocked(getFeishuNotificationConfig).mockResolvedValue({ configured: true, enabled: true, maskedDestination: '飞书机器人 …cdef' })
    vi.mocked(testFeishuNotification).mockResolvedValue({ id: 'delivery-1', status: 'pending', attempts: 0 })
    vi.mocked(getNotificationDelivery).mockResolvedValue({ id: 'delivery-1', status: 'sent', attempts: 1, sentAt: '2026-08-31T12:00:00Z' })
    const user = userEvent.setup()
    render(<NotificationSettingsPanel pollIntervalMs={1} />)

    await user.click(await screen.findByRole('button', { name: '发送测试消息' }))

    expect(await screen.findByText('飞书测试消息已发送')).toBeVisible()
    expect(getNotificationDelivery).toHaveBeenCalledWith('delivery-1')
  })

  it('失败时显示有界中文错误', async () => {
    vi.mocked(getFeishuNotificationConfig).mockResolvedValue({ configured: true, enabled: true, maskedDestination: '飞书机器人 …cdef' })
    vi.mocked(testFeishuNotification).mockResolvedValue({ id: 'delivery-1', status: 'pending', attempts: 0 })
    vi.mocked(getNotificationDelivery).mockResolvedValue({ id: 'delivery-1', status: 'failed', attempts: 24, lastError: '飞书消息发送失败' })
    const user = userEvent.setup()
    render(<NotificationSettingsPanel pollIntervalMs={1} />)

    await user.click(await screen.findByRole('button', { name: '发送测试消息' }))

    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('飞书消息发送失败'))
  })
})
