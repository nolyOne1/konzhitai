import { afterEach, describe, expect, it, vi } from 'vitest'

import { getDashboard } from './client'

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
})
