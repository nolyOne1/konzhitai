import { expect, test } from '@playwright/test'

import { mockQueueWakeup, queuedRunFixture } from './fixtures'

test('服务器资源释放后排队任务显示为已分配', async ({ page }) => {
  const queue = await mockQueueWakeup(page)
  await page.goto('/runs')

  const runRow = page.getByRole('row').filter({ hasText: queuedRunFixture.taskName })
  await expect(runRow.getByText(queuedRunFixture.taskName, { exact: true })).toBeVisible()
  await expect(runRow.getByText('排队等待', { exact: true })).toBeVisible()
  await expect(runRow.getByText('暂无分配服务器', { exact: true })).toBeVisible()

  queue.releaseResources()
  await page.reload()

  await expect(runRow.getByText('已分配', { exact: true })).toBeVisible()
  await expect(runRow.getByText('京东云执行节点', { exact: true })).toBeVisible()
})
