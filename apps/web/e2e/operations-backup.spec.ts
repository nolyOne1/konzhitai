import { expect, test } from '@playwright/test'

import { mockOperationsBackups } from './fixtures'

test('管理员手动备份、查看降级状态并从 COS 发起隔离恢复校验', async ({ page }) => {
  const requests = await mockOperationsBackups(page)
  await page.goto('/operations')

  await expect(page.getByText('下一次自动备份')).toBeVisible()
  await expect(page.getByText('COS 已同步').first()).toBeVisible()

  await page.getByRole('button', { name: '立即备份' }).click()
  await expect(page.getByRole('status')).toContainText('备份请求已进入队列')
  await expect(page.getByText('仅本机成功').first()).toBeVisible()
  expect(requests.backupIdempotencyKey()).toMatch(/^[0-9a-f-]{36}$/i)

  await page.getByRole('button', { name: '立即校验' }).click()
  await expect(page.getByRole('status')).toContainText('恢复校验请求已进入队列')
  expect(requests.verificationBody()).toEqual({ backupRunId: 'backup-cos-1' })
  expect(requests.verificationIdempotencyKey()).toMatch(/^[0-9a-f-]{36}$/i)
})
