import { expect, test } from '@playwright/test'

import { mockTaskRun, taskFixture } from './fixtures'

test('手动执行任务后显示已进入排队队列', async ({ page }) => {
  const requests = await mockTaskRun(page)
  await page.goto('/tasks')

  await expect(page.getByRole('heading', { name: '任务调度' })).toBeVisible()
  await expect(page.getByText(taskFixture.name, { exact: true })).toBeVisible()
  await page.getByRole('button', { name: `手动执行${taskFixture.name}` }).click()
  await expect(page.getByRole('dialog')).toBeVisible()
  await page.getByLabel('本次普通参数（JSON）').fill('{"保留天数": 30}')
  await page.getByRole('button', { name: '确认执行', exact: true }).click()

  await expect(page.getByRole('status')).toContainText(`${taskFixture.name}已进入排队队列`)
  await expect(page.getByRole('link', { name: '查看本次执行' })).toHaveAttribute('href', '/runs/run-queue-0001')
  expect(requests.triggerBody()).toEqual({ parameters: { 保留天数: 30 } })
})
