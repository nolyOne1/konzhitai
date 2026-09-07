import { expect, test, type Page, type Route } from '@playwright/test'

const release = {
  id: 'release-2', version: '0.2.0', status: 'available', recommended: true,
  release_notes: '稳定版', capabilities: ['self_upgrade_v1'], created_at: '2026-09-07T00:00:00Z',
  artifacts: [{ os: 'linux', arch: 'amd64', file_name: 'yunling-agent-linux-amd64.tar.gz', byte_size: 42, sha256: 'a'.repeat(64), download_url: '/agent.tar.gz' }],
}

const server = {
  id: 'server-1', name: '京东云执行节点-1', cloudProvider: '京东云', region: '华东 1', status: 'online',
  enabled: true, draining: false, labels: { 用途: '批处理' }, runtimes: ['bash'], agentVersion: '0.1.0',
  agentOS: 'linux', agentArch: 'amd64', agentCapabilities: ['self_upgrade_v1'], schedulingWeight: 100,
  cpuUsagePercent: 10, memoryTotalBytes: 8000, memoryAvailableBytes: 4000, diskTotalBytes: 10000,
  diskAvailableBytes: 5000, runningTasks: 0, lastSeenAt: '2026-09-07T00:00:00Z',
}

test('管理员用键盘创建代理升级计划且手机宽度无页面溢出', async ({ page }) => {
  let submitted: unknown
  await mockUpgradeConsole(page, (value) => { submitted = value })
  await page.setViewportSize({ width: 375, height: 760 })
  await page.goto('/servers/upgrades')

  await expect(page.getByRole('heading', { level: 1, name: '代理升级' })).toBeVisible()
  await expect(page.getByLabel('升级概况')).toContainText('待升级1')
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)

  await page.getByRole('button', { name: '创建升级计划' }).click()
  await expect(page.getByRole('radio', { name: /0.2.0/ })).toBeFocused()
  await page.keyboard.press('Space')
  await page.getByRole('button', { name: '下一步' }).click()
  await page.getByLabel(/京东云执行节点-1/).check()
  await page.getByRole('button', { name: '下一步' }).click()
  await page.getByLabel('后续每批服务器数').fill('2')
  await page.getByRole('button', { name: '启动升级计划' }).click()

  await expect(page.getByRole('heading', { name: '当前升级计划' })).toBeVisible()
  expect(submitted).toEqual({
    target_release_id: 'release-2', server_ids: ['server-1'], batch_size: 2,
    drain_timeout_seconds: 3600, reconnect_timeout_seconds: 120, verification_seconds: 30,
  })
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
})

async function mockUpgradeConsole(page: Page, onSubmit: (value: unknown) => void) {
  await page.route('**/api/auth/session', async (route) => json(route, { user: { user_id: 'admin-1', display_name: '管理员', email: 'admin@example.test', roles: ['admin'], must_change_password: false } }))
  await page.route('**/api/agent-releases', async (route) => json(route, { releases: [release] }))
  await page.route('**/api/servers', async (route) => json(route, { servers: [server] }))
  await page.route('**/api/agent-upgrades', async (route) => {
    if (route.request().method() === 'GET') {
      await json(route, { plans: [] })
      return
    }
    onSubmit(route.request().postDataJSON())
    await json(route, {
      id: 'plan-1', target_version: '0.2.0', status: 'running', current_batch: 1,
      created_at: '2026-09-07T00:00:00Z', pause_reason: '', events: [],
      targets: [{ id: 'target-1', server_id: 'server-1', server_name: '京东云执行节点-1', batch_number: 1, source_version: '0.1.0', target_version: '0.2.0', status: 'waiting', attempts: 0, error_message: '', updated_at: '2026-09-07T00:00:00Z' }],
    }, 201)
  })
}

async function json(route: Route, body: unknown, status = 200) {
  await route.fulfill({ status, contentType: 'application/json; charset=utf-8', body: JSON.stringify(body) })
}
