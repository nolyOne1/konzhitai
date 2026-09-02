import type { Page, Route } from '@playwright/test'

export const taskFixture = {
  id: 'task-archive',
  name: '每日归档任务',
  description: '归档业务日志并上传对象存储',
  scriptId: 'script-archive',
  scriptName: '日志归档脚本',
  versionPolicy: 'latest',
  parameters: {},
  secretRefs: {},
  priority: 80,
  requiredLabels: { 用途: '批处理' },
  requiredRuntime: 'bash',
  resources: { cpuMillicores: 1000, memoryBytes: 1073741824, diskBytes: 2147483648 },
  maxConcurrency: 1,
  timeoutSeconds: 1800,
  maxWaitSeconds: 3600,
  retryPolicy: { maxRetries: 1, backoffSeconds: 30 },
  idempotent: true,
  enabled: true,
  createdAt: '2026-08-28T10:00:00Z',
  updatedAt: '2026-08-28T10:00:00Z',
}

export const queuedRunFixture = {
  id: 'run-queue-0001',
  definitionId: taskFixture.id,
  taskName: taskFixture.name,
  scriptId: taskFixture.scriptId,
  scriptName: taskFixture.scriptName,
  scriptVersionId: 'version-archive-1',
  versionNumber: 1,
  triggerType: 'manual',
  state: 'queued',
  parameters: {},
  resources: taskFixture.resources,
  requiredRuntime: 'bash',
  priority: 80,
  attempt: 1,
  maxRetries: 1,
  idempotent: true,
  processConfirmedGone: false,
  queuedAt: '2026-08-28T12:00:00Z',
  resultSummary: '',
  createdAt: '2026-08-28T12:00:00Z',
}

export async function mockTaskRun(page: Page) {
  let triggerBody: unknown
  await page.route('**/api/tasks', async (route) => {
    if (route.request().method() === 'GET') {
      await json(route, { tasks: [taskFixture] })
      return
    }
    await route.fallback()
  })
  await page.route(`**/api/tasks/${taskFixture.id}/run`, async (route) => {
    triggerBody = route.request().postDataJSON()
    await json(route, queuedRunFixture, 201)
  })
  return { triggerBody: () => triggerBody }
}

export async function mockQueueWakeup(page: Page) {
  let released = false
  await page.route('**/api/runs', async (route) => {
    const run = released
      ? { ...queuedRunFixture, state: 'assigned', serverId: 'server-a', serverName: '京东云执行节点' }
      : queuedRunFixture
    await json(route, { runs: [run] })
  })
  return { releaseResources: () => { released = true } }
}

export async function mockPasswordChange(page: Page) {
  let requestBody: unknown
  await page.route('**/api/auth/password', async (route) => {
    if (route.request().method() !== 'POST') {
      await route.fallback()
      return
    }
    requestBody = route.request().postDataJSON()
    await route.fulfill({ status: 204 })
  })
  return { requestBody: () => requestBody }
}

export async function mockOperationsNotifications(page: Page) {
  let config = { configured: false, enabled: false, maskedDestination: '' }
  let updateBody: unknown
  await page.route('**/api/auth/session', async (route) => {
    await json(route, { user: { user_id: 'admin-1', display_name: '管理员', email: 'admin@example.com', roles: ['admin'] } })
  })
  await page.route('**/api/operations/notifications/feishu', async (route) => {
    if (route.request().method() === 'PUT') {
      updateBody = route.request().postDataJSON()
      config = { configured: true, enabled: true, maskedDestination: '飞书机器人 …cdef' }
    }
    await json(route, config)
  })
  await page.route('**/api/operations/notifications/feishu/test', async (route) => {
    await json(route, { id: 'delivery-1', status: 'pending', attempts: 0 }, 202)
  })
  await page.route('**/api/operations/notifications/delivery-1', async (route) => {
    await json(route, { id: 'delivery-1', status: 'sent', attempts: 1, sentAt: '2026-08-31T12:00:00Z' })
  })
  await mockEmptyBackupState(page)
  return { updateBody: () => updateBody }
}

export async function mockOperationsBackups(page: Page) {
  let degraded = false
  let queuedVerification: typeof verificationFixture | undefined
  let backupIdempotencyKey = ''
  let verificationIdempotencyKey = ''
  let verificationBody: unknown

  await page.route('**/api/auth/session', async (route) => {
    await json(route, { user: { user_id: 'admin-1', display_name: '管理员', email: 'admin@example.com', roles: ['admin'] } })
  })
  await page.route('**/api/operations/notifications/feishu', async (route) => {
    await json(route, { configured: false, enabled: false, maskedDestination: '' })
  })
  await page.route('**/api/operations/summary', async (route) => {
    await json(route, {
      status: degraded ? 'degraded' : 'healthy',
      nextBackupAt: '2026-09-01T00:30:00+08:00',
      latestLocalBackup: degraded ? degradedBackupFixture : successfulBackupFixture,
      latestCOSBackup: successfulBackupFixture,
      latestVerification: queuedVerification ?? successfulVerificationFixture,
    })
  })
  await page.route('**/api/operations/backups', async (route) => {
    if (route.request().method() === 'POST') {
      backupIdempotencyKey = route.request().headers()['idempotency-key'] ?? ''
      degraded = true
      await json(route, { ...degradedBackupFixture, status: 'queued' }, 202)
      return
    }
    await json(route, { backups: degraded ? [degradedBackupFixture, successfulBackupFixture] : [successfulBackupFixture] })
  })
  await page.route('**/api/operations/verifications', async (route) => {
    if (route.request().method() === 'POST') {
      verificationIdempotencyKey = route.request().headers()['idempotency-key'] ?? ''
      verificationBody = route.request().postDataJSON()
      queuedVerification = verificationFixture
      await json(route, verificationFixture, 202)
      return
    }
    await json(route, { verifications: queuedVerification ? [queuedVerification, successfulVerificationFixture] : [successfulVerificationFixture] })
  })

  return {
    backupIdempotencyKey: () => backupIdempotencyKey,
    verificationIdempotencyKey: () => verificationIdempotencyKey,
    verificationBody: () => verificationBody,
  }
}

const successfulBackupFixture = {
  id: 'backup-cos-1', triggerType: 'scheduled', status: 'succeeded', scheduledFor: '2026-08-31T18:30:00+08:00',
  localSnapshotId: 'local-1', cosSnapshotId: 'cos-1', manifestSha256: 'a'.repeat(64), byteSize: 3145728,
  objectCount: 12, attempts: 1, nextAttemptAt: '2026-08-31T18:30:00+08:00',
  startedAt: '2026-08-31T18:30:01+08:00', finishedAt: '2026-08-31T18:31:00+08:00',
  createdAt: '2026-08-31T18:30:01+08:00', updatedAt: '2026-08-31T18:31:00+08:00',
}

const degradedBackupFixture = {
  id: 'backup-local-2', triggerType: 'manual', status: 'degraded', localSnapshotId: 'local-2',
  manifestSha256: 'b'.repeat(64), byteSize: 4194304, objectCount: 14, attempts: 1,
  nextAttemptAt: '2026-08-31T19:05:00+08:00', startedAt: '2026-08-31T19:00:00+08:00',
  finishedAt: '2026-08-31T19:01:00+08:00', createdAt: '2026-08-31T19:00:00+08:00', updatedAt: '2026-08-31T19:01:00+08:00',
}

const successfulVerificationFixture = {
  id: 'verification-1', backupRunId: 'backup-cos-1', triggerType: 'scheduled', status: 'succeeded',
  migrationVersion: '12', checkedObjects: 12, startedAt: '2026-08-31T18:35:00+08:00',
  finishedAt: '2026-08-31T18:36:00+08:00', createdAt: '2026-08-31T18:35:00+08:00', updatedAt: '2026-08-31T18:36:00+08:00',
}

const verificationFixture = {
  id: 'verification-2', backupRunId: 'backup-cos-1', triggerType: 'manual', status: 'queued',
  checkedObjects: 0, createdAt: '2026-08-31T19:02:00+08:00', updatedAt: '2026-08-31T19:02:00+08:00',
}

async function mockEmptyBackupState(page: Page) {
  await page.route('**/api/operations/summary', async (route) => {
    await json(route, { status: 'not_started', nextBackupAt: '2026-09-01T00:30:00+08:00', latestLocalBackup: null, latestCOSBackup: null, latestVerification: null })
  })
  await page.route('**/api/operations/backups', async (route) => { await json(route, { backups: [] }) })
  await page.route('**/api/operations/verifications', async (route) => { await json(route, { verifications: [] }) })
}

async function json(route: Route, body: unknown, status = 200) {
  await route.fulfill({
    status,
    contentType: 'application/json; charset=utf-8',
    body: JSON.stringify(body),
  })
}
