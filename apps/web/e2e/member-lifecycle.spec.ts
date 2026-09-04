import { expect, test } from '@playwright/test'

import { mockMemberLifecycle } from './fixtures'

test('管理员创建成员并完成首次改密流程', async ({ page }) => {
  await mockMemberLifecycle(page)
  await page.goto('/members')
  await page.getByRole('button', { name: '创建成员' }).click()
  await page.getByLabel('姓名').fill('值班运维')
  await page.getByLabel('邮箱').fill('ops@example.com')
  await page.getByRole('checkbox', { name: '运维人员' }).check()
  await page.getByRole('button', { name: '确认创建' }).click()
  await expect(page.getByRole('dialog', { name: '一次性临时密码' })).toBeVisible()
  await page.getByRole('button', { name: '我已保存，关闭' }).click()
  await page.goto('/password-required')
  await expect(page.getByRole('heading', { name: '首次登录，请修改密码' })).toBeVisible()
})

test('管理员停用、移除、恢复成员并确认重置密码', async ({ page }) => {
  await mockMemberLifecycle(page)
  await page.goto('/members')
  await page.getByRole('button', { name: '更多值班运维操作' }).click()
  await page.getByRole('menuitem', { name: '停用成员' }).click()
  await expect(page.getByRole('dialog', { name: '确认停用成员' })).toBeVisible()
  await page.getByRole('button', { name: '确认停用', exact: true }).click()
  await expect(page.getByRole('status')).toContainText('值班运维已停用')

  await page.getByRole('button', { name: '更多值班运维操作' }).click()
  await page.getByRole('menuitem', { name: '重置密码' }).click()
  await expect(page.getByRole('dialog', { name: '确认重置密码' })).toContainText('所有现有会话会立即失效')
  await page.getByRole('button', { name: '确认重置密码', exact: true }).click()
  await expect(page.getByRole('dialog', { name: '一次性临时密码' })).toBeVisible()
  await page.getByRole('button', { name: '我已保存，关闭' }).click()

  await page.getByRole('button', { name: '更多值班运维操作' }).click()
  await page.getByRole('menuitem', { name: '移除成员' }).click()
  await page.getByRole('button', { name: '确认移除', exact: true }).click()
  await expect(page.getByText('值班运维', { exact: true })).not.toBeVisible()

  await page.getByRole('button', { name: '已移除' }).click()
  await expect(page.getByRole('button', { name: '已移除' })).toHaveAttribute('aria-pressed', 'true')
  await expect(page.getByText('值班运维', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: '更多值班运维操作' }).click()
  await page.getByRole('menuitem', { name: '恢复成员' }).click()
  await expect(page.getByRole('dialog', { name: '确认恢复成员' })).toContainText('账号会保持停用状态')
  await page.getByRole('button', { name: '确认恢复', exact: true }).click()
  await expect(page.getByRole('table').getByText('已停用', { exact: true })).toBeVisible()
})
