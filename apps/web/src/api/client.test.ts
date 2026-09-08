import { afterEach, describe, expect, it, vi } from 'vitest'

import {
  changePassword,
  cancelAgentUpgradePlan,
  createAgentUpgradePlan,
  createMember,
  getBackups,
  getBackupSummary,
  getDashboard,
  getFeishuNotificationConfig,
  getAgentReleases,
  getAgentUpgradePlan,
  getAgentUpgradePlans,
  getLatestAgentRelease,
  getMembers,
  getSession,
  getNotificationDelivery,
  getRestoreVerifications,
  pauseAgentUpgradePlan,
  recommendAgentRelease,
  requestBackup,
  requestVerification,
  resumeAgentUpgradePlan,
  retryAgentUpgradeTarget,
  rollbackAgentUpgradeTarget,
  removeMember,
  resetMemberPassword,
  restoreMember,
  setMemberEnabled,
  testFeishuNotification,
  updateFeishuNotificationConfig,
  withdrawAgentRelease,
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

  it('读取管理端代理版本并映射发布字段', async () => {
    const fetchMock = vi.fn().mockResolvedValue(response({ releases: [{
      id: 'release-2', version: '0.2.0', status: 'available', recommended: true,
      release_notes: '稳定版', capabilities: ['self_upgrade_v1'], created_at: '2026-09-07T00:00:00Z',
      artifacts: [{ os: 'linux', arch: 'amd64', file_name: 'agent.tar.gz', byte_size: 42, sha256: 'a'.repeat(64), download_url: '/download' }],
    }] }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(getAgentReleases()).resolves.toEqual([{
      id: 'release-2', version: '0.2.0', status: 'available', recommended: true,
      releaseNotes: '稳定版', capabilities: ['self_upgrade_v1'], createdAt: '2026-09-07T00:00:00Z',
      artifacts: [{ os: 'linux', arch: 'amd64', fileName: 'agent.tar.gz', byteSize: 42, sha256: 'a'.repeat(64), downloadUrl: '/download' }],
    }])
    expect(fetchMock).toHaveBeenCalledWith('/api/agent-releases', { credentials: 'same-origin' })
  })

  it('使用固定路径管理代理升级计划和版本', async () => {
    const rawPlan = {
      id: 'plan-1', target_version: '0.2.0', status: 'running', current_batch: 1,
      created_at: '2026-09-07T00:00:00Z', targets: [], events: [],
    }
    const fetchMock = vi.fn().mockResolvedValue(response({ plans: [rawPlan] }))
      .mockResolvedValueOnce(response({ plans: [rawPlan] }))
      .mockResolvedValueOnce(response(rawPlan))
      .mockResolvedValueOnce(response(rawPlan, 201))
      .mockResolvedValueOnce(response(rawPlan))
      .mockResolvedValueOnce(response(rawPlan))
      .mockResolvedValueOnce(response(rawPlan))
      .mockResolvedValueOnce(response(rawPlan))
      .mockResolvedValueOnce(response(rawPlan, 201))
      .mockResolvedValueOnce(response({ id: 'release-2' }))
      .mockResolvedValueOnce(response({ id: 'release-1' }))
    vi.stubGlobal('fetch', fetchMock)

    await getAgentUpgradePlans()
    await getAgentUpgradePlan('plan/1')
    await createAgentUpgradePlan({ targetReleaseId: 'release-2', serverIds: ['server-1'], batchSize: 2, drainTimeoutSeconds: 3600, reconnectTimeoutSeconds: 120, verificationSeconds: 30 })
    await pauseAgentUpgradePlan('plan/1', '人工暂停')
    await resumeAgentUpgradePlan('plan/1')
    await cancelAgentUpgradePlan('plan/1')
    await retryAgentUpgradeTarget('plan/1', 'target/1')
    await rollbackAgentUpgradeTarget('plan/1', 'target/1')
    await recommendAgentRelease('release/2')
    await withdrawAgentRelease('release/1')

    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/agent-upgrades', { credentials: 'same-origin' })
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/agent-upgrades/plan%2F1', { credentials: 'same-origin' })
    expect(fetchMock).toHaveBeenNthCalledWith(3, '/api/agent-upgrades', {
      method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ target_release_id: 'release-2', server_ids: ['server-1'], batch_size: 2, drain_timeout_seconds: 3600, reconnect_timeout_seconds: 120, verification_seconds: 30 }),
    })
    expect(fetchMock).toHaveBeenNthCalledWith(4, '/api/agent-upgrades/plan%2F1/pause', { method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ reason: '人工暂停' }) })
    expect(fetchMock).toHaveBeenNthCalledWith(5, '/api/agent-upgrades/plan%2F1/resume', { method: 'POST', credentials: 'same-origin' })
    expect(fetchMock).toHaveBeenNthCalledWith(6, '/api/agent-upgrades/plan%2F1/cancel', { method: 'POST', credentials: 'same-origin' })
    expect(fetchMock).toHaveBeenNthCalledWith(7, '/api/agent-upgrades/plan%2F1/targets/target%2F1/retry', { method: 'POST', credentials: 'same-origin' })
    expect(fetchMock).toHaveBeenNthCalledWith(8, '/api/agent-upgrades/plan%2F1/targets/target%2F1/rollback', { method: 'POST', credentials: 'same-origin' })
    expect(fetchMock).toHaveBeenNthCalledWith(9, '/api/agent-releases/release%2F2/recommend', { method: 'POST', credentials: 'same-origin' })
    expect(fetchMock).toHaveBeenNthCalledWith(10, '/api/agent-releases/release%2F1/withdraw', { method: 'POST', credentials: 'same-origin' })
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

  it('创建成员并读取一次性临时密码', async () => {
    const member = memberFixture()
    const fetchMock = vi.fn().mockResolvedValue(response({ member, temporaryPassword: 'temporary-password' }, 201))
    vi.stubGlobal('fetch', fetchMock)
    const result = await createMember({ displayName: '值班运维', email: 'ops@example.com', roles: ['operator'] })
    expect(result.temporaryPassword).toBe('temporary-password')
    expect(fetchMock).toHaveBeenCalledWith('/api/members', {
      method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ displayName: '值班运维', email: 'ops@example.com', roles: ['operator'] }),
    })
  })

  it('按状态编码读取成员', async () => {
    const member = memberFixture()
    const fetchMock = vi.fn().mockResolvedValue(response({ members: [member] }))
    vi.stubGlobal('fetch', fetchMock)
    await expect(getMembers('removed')).resolves.toEqual([member])
    expect(fetchMock).toHaveBeenCalledWith('/api/members?status=removed', { credentials: 'same-origin' })
  })

  it('请求成员启停用、移除、恢复和重置密码路径', async () => {
    const member = memberFixture()
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(response(member)).mockResolvedValueOnce(response(member))
      .mockResolvedValueOnce(response(member, 204)).mockResolvedValueOnce(response(member))
      .mockResolvedValueOnce(response({ member, temporaryPassword: 'new-temporary-password' }))
    vi.stubGlobal('fetch', fetchMock)
    await setMemberEnabled('member/1', true)
    await setMemberEnabled('member/1', false)
    await removeMember('member/1')
    await restoreMember('member/1')
    await expect(resetMemberPassword('member/1')).resolves.toEqual({ member, temporaryPassword: 'new-temporary-password' })
    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/members/member%2F1/enable', { method: 'POST', credentials: 'same-origin' })
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/members/member%2F1/disable', { method: 'POST', credentials: 'same-origin' })
    expect(fetchMock).toHaveBeenNthCalledWith(3, '/api/members/member%2F1', { method: 'DELETE', credentials: 'same-origin' })
    expect(fetchMock).toHaveBeenNthCalledWith(4, '/api/members/member%2F1/restore', { method: 'POST', credentials: 'same-origin' })
    expect(fetchMock).toHaveBeenNthCalledWith(5, '/api/members/member%2F1/password/reset', { method: 'POST', credentials: 'same-origin' })
  })

  it('将会话用户的蛇形改密字段映射为驼峰字段', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response({ user: {
      user_id: 'user-1', display_name: '管理员', email: 'admin@example.com', roles: ['admin'], must_change_password: true,
    } })))
    await expect(getSession()).resolves.toEqual({ id: 'user-1', displayName: '管理员', email: 'admin@example.com', roles: ['admin'], mustChangePassword: true })
  })
})

function response(body: unknown, status = 200) {
  return { ok: true, status, json: async () => body } as Response
}

function memberFixture() {
  return { id: 'member-1', email: 'ops@example.com', displayName: '值班运维', enabled: true, roles: ['operator'] as const, mustChangePassword: true, removedAt: null, createdAt: '2026-09-04T00:00:00Z' }
}
