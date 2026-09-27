import { expect, test } from '@playwright/test'
import { readFile } from 'node:fs/promises'
import { createHash } from 'node:crypto'
import { gzipSync } from 'node:zlib'
import { mockQueueWakeup, queuedRunFixture } from './fixtures'

test('总览显示当前任务和最新同步统计，手机宽度无溢出', async ({ page }) => {
  await mockQueueWakeup(page)
  await page.route('**/api/dashboard', (route) => route.fulfill({ json: {
    onlineServers: 1, totalServers: 2, runningRuns: 1, queuedRuns: 3, todaySuccessRate: 98,
    servers: [], recentEvents: [{ id: 'event-1', type: 'run.started', message: '订单同步已开始', occurredAt: '2026-09-23T08:30:00Z' }],
    activeRuns: [{ id: 'run-live', taskName: 'ERP订单同步', scriptName: '同步销售订单', serverName: '华东执行节点', state: 'running', resultSummary: '正在同步订单', queuedAt: '2026-09-23T08:30:00Z' }],
    scriptSync: { publishedScripts: 2, total: 3, ready: 1, pending: 0, downloading: 0, failed: 1, drifted: 1 },
  } }))
  await page.goto('/')
  await expect(page.getByRole('heading', { name: '运行总览' })).toBeVisible()
  await expect(page.getByRole('link', { name: 'ERP订单同步' })).toHaveAttribute('href', '/runs/run-live')
  await expect(page.getByLabel('当前版本同步状态')).toContainText('失败 1')
  await page.screenshot({ path: test.info().outputPath('dashboard-desktop.png'), fullPage: true })
  await page.setViewportSize({ width: 375, height: 812 })
  await expect(page.getByRole('link', { name: 'ERP订单同步' })).toBeVisible()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
  await page.screenshot({ path: test.info().outputPath('dashboard-mobile.png'), fullPage: true })
})

test('执行详情展示真实采样、归档与产物并下载完整附件', async ({ page }) => {
  await mockQueueWakeup(page)
  const id = queuedRunFixture.id
  const report = Buffer.from('订单号,状态\n123,完成\n')
  const log = Buffer.from('[stdout] 完整日志\n')
  const archive = gzipSync(Buffer.from('{"content":"完整日志"}\n'))
  await page.route(`**/api/runs/${id}`, (route) => route.fulfill({ json: {
    ...queuedRunFixture, state: 'succeeded', finishedAt: '2026-09-23T08:30:00Z', exitCode: 0,
    usage: { cpuTimeMillis: 2100, memoryBytes: 1048576, peakMemoryBytes: 2097152, processes: 2, sampledAt: '2026-09-23T08:29:59Z' },
  } }))
  await page.route(`**/api/runs/${id}/events*`, (route) => route.fulfill({ contentType: 'text/event-stream', body: ': ready\n\n' }))
  await page.route(`**/api/runs/${id}/logs/archive/info`, (route) => route.fulfill({ json: { available: true, current: true, ...digest(archive), chunkCount: 3, archivedAt: '2026-09-23T08:31:00Z' } }))
  await page.route(`**/api/runs/${id}/logs/archive`, (route) => route.fulfill({ contentType: 'application/gzip', body: archive }))
  await page.route(`**/api/runs/${id}/logs`, (route) => route.fulfill({ contentType: 'text/plain; charset=utf-8', body: log }))
  await page.route(`**/api/runs/${id}/artifacts`, (route) => route.fulfill({ json: { artifacts: [{ id: 'artifact-1', runId: id, name: '订单结果.csv', ...digest(report), createdAt: '2026-09-23T08:31:00Z' }] } }))
  await page.route(`**/api/runs/${id}/artifacts/artifact-1`, (route) => route.fulfill({ contentType: 'application/octet-stream', body: report }))
  await page.goto(`/runs/${id}`)
  await expect(page.getByRole('heading', { name: '实际资源使用' })).toBeVisible()
  await expect(page.getByText('2.10 秒')).toBeVisible()
  await expect(page.getByRole('button', { name: '下载 订单结果.csv' })).toBeVisible()
  for (const file of [
    { button: '下载 订单结果.csv', filename: '订单结果.csv', body: report },
    { button: '下载日志', filename: `${id}.log`, body: log },
    { button: '下载压缩归档', filename: `${id}.logs.ndjson.gz`, body: archive },
  ]) {
    const downloadPromise = page.waitForEvent('download')
    await page.getByRole('button', { name: file.button, exact: true }).click()
    const download = await downloadPromise
    expect(download.suggestedFilename()).toBe(file.filename)
    expect(await readFile((await download.path())!)).toEqual(file.body)
    await expect(page.getByRole('status').filter({ hasText: '发起下载' })).toBeVisible()
  }
  await page.setViewportSize({ width: 375, height: 812 })
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
  await page.screenshot({ path: test.info().outputPath('run-detail-mobile.png'), fullPage: true })
})

test('产物可安全读取 UTF-8 预览，超限仅下载且手机无溢出', async ({ page }) => {
  await mockQueueWakeup(page)
  const id = queuedRunFixture.id
  const contents = '中文内容\n<script>window.previewExecuted = true</script>\n<img src=x onerror="window.previewExecuted=true">\n' + '长字段'.repeat(200)
  const report = Buffer.from(contents)
  await page.route(`**/api/runs/${id}`, (route) => route.fulfill({ json: { ...queuedRunFixture, state: 'succeeded' } }))
  await page.route(`**/api/runs/${id}/events*`, (route) => route.fulfill({ contentType: 'text/event-stream', body: ': ready\n\n' }))
  await page.route(`**/api/runs/${id}/logs/archive/info`, (route) => route.fulfill({ json: { available: false, current: false } }))
  await page.route(`**/api/runs/${id}/artifacts`, (route) => route.fulfill({ json: { artifacts: [
    { id: 'text-1', name: '验收结果.txt', ...digest(report), createdAt: '2026-09-23T08:31:00Z' },
    { id: 'large-1', name: '大文件.txt', byteSize: 256 * 1024 + 1, sha256: 'a'.repeat(64), createdAt: '2026-09-23T08:31:00Z' },
  ] } }))
  await page.route(`**/api/runs/${id}/artifacts/text-1`, (route) => route.fulfill({ contentType: 'application/octet-stream', body: report }))
  await page.goto(`/runs/${id}`)
  await page.getByRole('button', { name: '预览 验收结果.txt' }).click()
  const preview = page.getByLabel('文件文本内容')
  await expect(preview).toHaveText(contents)
  expect(await preview.locator('script, img').count()).toBe(0)
  expect(await page.evaluate(() => 'previewExecuted' in window)).toBe(false)
  await expect(page.getByText(/已验证大小/)).toContainText(digest(report).sha256)
  await expect(page.getByRole('button', { name: '预览 大文件.txt' })).toBeDisabled()
  await expect(page.getByRole('button', { name: '下载 大文件.txt' })).toBeEnabled()
  await page.setViewportSize({ width: 375, height: 812 })
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
  const close = page.getByRole('button', { name: '关闭预览' })
  expect((await close.boundingBox())!.height).toBeGreaterThanOrEqual(44)
  await page.screenshot({ path: test.info().outputPath('artifact-preview-mobile.png'), fullPage: true })
  await close.click()
  await expect(preview).toHaveCount(0)
  await expect(page.getByRole('button', { name: '预览 验收结果.txt' })).toBeFocused()
})

function digest(body: Buffer) { return { byteSize: body.byteLength, sha256: createHash('sha256').update(body).digest('hex') } }
