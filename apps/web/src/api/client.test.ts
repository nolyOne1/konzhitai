import { afterEach, describe, expect, it, vi } from 'vitest'

import { changePassword, getDashboard } from './client'

describe('API 客户端', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('将非 JSON 成功响应转换为中文错误', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: true,
      json: async () => { throw new SyntaxError("Unexpected token '<'") },
    }))

    await expect(getDashboard()).rejects.toThrow('服务返回的数据格式不正确')
  })

  it('使用同源 JSON 请求修改当前用户密码', async () => {
	const fetchMock = vi.fn().mockResolvedValue({ ok: true, status: 204 })
	vi.stubGlobal('fetch', fetchMock)

	await changePassword('current-password', 'new-password-2026')

	expect(fetchMock).toHaveBeenCalledWith('/api/auth/password', {
	  method: 'POST',
	  credentials: 'same-origin',
	  headers: { 'Content-Type': 'application/json' },
	  body: JSON.stringify({ currentPassword: 'current-password', newPassword: 'new-password-2026' }),
	})
  })
})
