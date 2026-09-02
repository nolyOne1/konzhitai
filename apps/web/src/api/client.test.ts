import { afterEach, describe, expect, it, vi } from 'vitest'

import {
  changePassword,
  getBackups,
  getBackupSummary,
  getDashboard,
  getFeishuNotificationConfig,
  getLatestAgentRelease,
  getNotificationDelivery,
  getRestoreVerifications,
  requestBackup,
  requestVerification,
  testFeishuNotification,
  updateFeishuNotificationConfig,
} from './client'

describe('API 客户端', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('将非 JSON 成功响应转换为中文错误', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: true,
      json: async () => { throw new SyntaxError("Unexpected token '<'") },
    }))

    await expect(getDashboard()).rejects.toThrow('服务返回的数据格式不正确')
  })

  it('显式映射代理发布清单字段', async () => {
    const digest = 'a'.repeat(64)
    const fetchMock = vi.fn().mockResolvedValue(response({
      version: '0.1.0',
      artifacts: [{
        os: 'linux', arch: 'amd64', file_name: 'agent-amd64.tar.gz',
        byte_size: 42, sha256: digest,
        download_url: `/api/releases/agent/0.1.0/${digest}/agent-amd64.tar.gz`,
      }],
    }))
    vi.stubGlobal('fetch', fetchMock)

    const release = await getLatestAgentRelease()

    expect(release).toEqual({
      version: '0.1.0',
      artifacts: [{
        os: 'linux', arch: 'amd64', fileName: 'agent-amd64.tar.gz',
        byteSize: 42, sha256: digest,
        downloadUrl: `/api/releases/agent/0.1.0/${digest}/agent-amd64.tar.gz`,
      }],
    })
    expect(fetchMock).toHaveBeenCalledWith('/api/releases/agent/latest', { credentials: 'same-origin' })
  })

  it('使用同源 JSON 请求修改当前用户密码', async () => {
	  const fetchMock = vi.fn().mockResolvedValue({ ok: true, status: 204 })
	  vi.stubGlobal('fetch', fetchMock)

	  await changePassword('current-password', 'new-password-2026')

	  expect(fetchMock).toHaveBeenCalledWith('/api/auth/password', {
	    method: 'POST',
	    credentials: 'same-origin',
	    headers: { 'Content-Type': 'application/json' },
	    body: JSON.stringify({ currentPassword: 'current-password', newPassword: 'new-password-2026' }),
	  })
  })

  it('使用固定运维路径读取、更新并测试飞书通知', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(response({ configured: false, enabled: false, maskedDestination: '' }))
      .mockResolvedValueOnce(response({ configured: true, enabled: true, maskedDestination: '飞书机器人 …cdef' }))
      .mockResolvedValueOnce(response({ id: 'delivery-1', status: 'pending', attempts: 0 }, 202))
      .mockResolvedValueOnce(response({ id: 'delivery-1', status: 'sent', attempts: 1, sentAt: '2026-08-31T12:00:00Z' }))
    vi.stubGlobal('fetch', fetchMock)

    await getFeishuNotificationConfig()
    await updateFeishuNotificationConfig({ enabled: true, webhook: 'https://open.feishu.cn/hook/test', signingSecret: 'secret' })
    await testFeishuNotification()
    await getNotificationDelivery('delivery-1')

    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/operations/notifications/feishu', { credentials: 'same-origin' })
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/operations/notifications/feishu', {
      method: 'PUT', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ enabled: true, webhook: 'https://open.feishu.cn/hook/test', signingSecret: 'secret' }),
    })
    expect(fetchMock).toHaveBeenNthCalledWith(3, '/api/operations/notifications/feishu/test', {
      method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: '{}',
    })
    expect(fetchMock).toHaveBeenNthCalledWith(4, '/api/operations/notifications/delivery-1', { credentials: 'same-origin' })
  })

  it('使用 UUID 幂等键请求备份和恢复校验', async () => {
    vi.stubGlobal('crypto', { randomUUID: () => '33333333-3333-4333-8333-333333333333' })
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(response({ status: 'not_started', nextBackupAt: null, latestLocalBackup: null, latestCOSBackup: null, latestVerification: null }))
      .mockResolvedValueOnce(response({ backups: [] }))
      .mockResolvedValueOnce(response({ verifications: [] }))
      .mockResolvedValueOnce(response({ id: 'backup-1', status: 'queued' }, 202))
      .mockResolvedValueOnce(response({ id: 'verify-1', status: 'queued' }, 202))
    vi.stubGlobal('fetch', fetchMock)

    await getBackupSummary(); await getBackups(); await getRestoreVerifications(); await requestBackup(); await requestVerification('backup-1')

    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/operations/summary', { credentials: 'same-origin' })
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/operations/backups', { credentials: 'same-origin' })
    expect(fetchMock).toHaveBeenNthCalledWith(3, '/api/operations/verifications', { credentials: 'same-origin' })
    expect(fetchMock).toHaveBeenNthCalledWith(4, '/api/operations/backups', {
      method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json', 'Idempotency-Key': '33333333-3333-4333-8333-333333333333' }, body: '{}',
    })
    expect(fetchMock).toHaveBeenNthCalledWith(5, '/api/operations/verifications', {
      method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json', 'Idempotency-Key': '33333333-3333-4333-8333-333333333333' }, body: JSON.stringify({ backupRunId: 'backup-1' }),
    })
  })
})

function response(body: unknown, status = 200) {
  return { ok: true, status, json: async () => body } as Response
}
