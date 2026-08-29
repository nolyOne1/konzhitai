import { expect, test } from '@playwright/test'

import { mockTaskRun, taskFixture } from './fixtures'

test('手动执行任务后显示已进入排队队列', async ({ page }) => {
  const requests = await mockTaskRun(page)
  await page.goto('/tasks')

  await expect(page.getByRole('heading', { name: '任务调度' })).toBeVisible()
  await expect(page.getByText(taskFixture.name, { exact: true })).toBeVisible()
  await page.getByRole('button', { name: `手动执行${taskFixture.name}` }).click()

  await expect(page.getByRole('status')).toHaveText(`${taskFixture.name}已进入排队队列`)
  expect(requests.triggerBody()).toEqual({ parameters: {} })
})
