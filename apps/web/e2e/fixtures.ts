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

async function json(route: Route, body: unknown, status = 200) {
  await route.fulfill({
    status,
    contentType: 'application/json; charset=utf-8',
    body: JSON.stringify(body),
  })
}
