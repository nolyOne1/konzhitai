import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { SyncStatusPanel } from './SyncStatusPanel'

describe('脚本同步状态', () => {
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it('显示中文状态并允许重试失败节点', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(response({ syncs: [{
        id: 'sync-1', serverId: 'server-1', serverName: '批处理节点', scriptId: 'script-1',
        versionId: 'version-2', versionNumber: 2, state: 'failed', artifactSha256: 'a'.repeat(64),
        errorCode: 'checksum_mismatch', errorMessage: '脚本包校验失败', blocked: true,
        syncedAt: null, updatedAt: '2026-08-28T08:00:00Z',
      }] }))
      .mockResolvedValueOnce(noContentResponse())
      .mockResolvedValueOnce(response({ syncs: [] }))
    vi.stubGlobal('fetch', fetchMock)

    render(<SyncStatusPanel scriptId="script-1" />)

    expect(await screen.findByRole('heading', { name: '服务器同步状态' })).toBeVisible()
    expect(screen.getByText('同步失败')).toBeVisible()
    expect(screen.getByText('脚本包校验失败')).toBeVisible()
    await userEvent.click(screen.getByRole('button', { name: '重试批处理节点' }))
    expect(fetchMock).toHaveBeenCalledWith('/api/scripts/script-1/syncs/sync-1/retry', expect.objectContaining({ method: 'POST' }))
  })

  it('当前发布版本没有同步记录时不误报旧版本已就绪', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response({ syncs: [{ ...sync, versionId: 'old-version', state: 'ready' }] })))
    render(<SyncStatusPanel scriptId="script-1" currentVersionId="new-version" />)
    expect(await screen.findByText('当前版本按需分发，或暂时没有符合运行环境和标签的在线服务器。')).toBeVisible()
    expect(screen.queryByText('批处理节点')).not.toBeInTheDocument()
    expect(screen.queryByText('1 / 1 已就绪')).not.toBeInTheDocument()
  })

  it('持续刷新就绪后的漂移与自动重试进度', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(response({ syncs: [{ ...sync, state: 'ready' }] }))
      .mockResolvedValue(response({ syncs: [{ ...sync, state: 'failed', failureCount: 3, retryLimit: 3, nextRetryAt: null }] }))
    vi.stubGlobal('fetch', fetchMock)
    render(<SyncStatusPanel scriptId="script-1" currentVersionId="version-2" pollIntervalMs={100} />)
    expect(await screen.findByText('已就绪')).toBeVisible()
    expect(await screen.findByText(/已达到重试上限/)).toBeVisible()
    expect(screen.getByRole('button', { name: '重试批处理节点' })).toBeEnabled()
  })
})

const sync = {
  id: 'sync-1', serverId: 'server-1', serverName: '批处理节点', scriptId: 'script-1',
  versionId: 'version-2', versionNumber: 2, artifactSha256: 'a'.repeat(64),
  errorCode: '', errorMessage: '', blocked: false, syncedAt: null, updatedAt: '2026-08-28T08:00:00Z',
}

function response(value: unknown) {
  return Promise.resolve({ ok: true, json: async () => value })
}

function noContentResponse() {
  return Promise.resolve({ ok: true, status: 204, json: async () => { throw new Error('无响应体') } })
}
