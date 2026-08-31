import { expect, test } from '@playwright/test'

import { mockOperationsNotifications } from './fixtures'

const webhook = 'https://open.feishu.cn/open-apis/bot/v2/hook/01234567-89ab-cdef-0123-456789abcdef'

test('管理员保存脱敏飞书配置并发送测试消息', async ({ page }) => {
  const requests = await mockOperationsNotifications(page)
  await page.goto('/operations')
  await page.getByLabel('飞书 Webhook').fill(webhook)
  await page.getByLabel('签名密钥').fill('signing-secret')
  await page.getByLabel('启用飞书通知').check()
  await page.getByRole('button', { name: '保存通知设置' }).click()

  await expect(page.getByText('飞书机器人 …cdef')).toBeVisible()
  expect(requests.updateBody()).toEqual({ enabled: true, webhook, signingSecret: 'signing-secret' })
  await page.reload()
  await expect(page.getByText('飞书机器人 …cdef')).toBeVisible()
  await expect(page.locator('body')).not.toContainText(webhook)
  await expect(page.locator('body')).not.toContainText('signing-secret')

  await page.getByRole('button', { name: '发送测试消息' }).click()
  await expect(page.getByRole('status')).toContainText('飞书测试消息已发送')
})
