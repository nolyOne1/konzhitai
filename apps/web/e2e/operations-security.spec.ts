import { expect, test } from '@playwright/test'

import { mockPasswordChange } from './fixtures'

test('管理员改密后看到其他设备已退出提示', async ({ page }) => {
  const requests = await mockPasswordChange(page)
  await page.goto('/operations')
  await page.getByLabel('当前密码').fill('current-password')
  await page.getByLabel('新密码', { exact: true }).fill('new-password-2026')
  await page.getByLabel('确认新密码').fill('new-password-2026')
  await page.getByRole('button', { name: '更新密码' }).click()

  await expect(page.getByRole('status')).toContainText('其他设备已退出')
  expect(requests.requestBody()).toEqual({
    currentPassword: 'current-password',
    newPassword: 'new-password-2026',
  })
})
