import { cleanup, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { getScripts, getScriptSyncs, retryScriptSync } from '../../api/client'
import { SyncPage } from './SyncPage'

vi.mock('../../api/client', () => ({
  getScripts: vi.fn(),
  getScriptSyncs: vi.fn(),
  retryScriptSync: vi.fn(),
}))

describe('脚本同步总览', () => {
  beforeEach(() => {
    vi.mocked(getScripts).mockResolvedValue([
      { id: 'script-1', name: '订单同步', description: '同步订单状态', runtime: 'bash', category: '业务', tags: ['订单'], currentVersionId: 'version-2', currentVersion: 2, draftUpdatedAt: null, createdAt: '2026-09-01T08:00:00Z', updatedAt: '2026-09-02T08:00:00Z' },
      { id: 'script-draft', name: '草稿脚本', description: '', runtime: 'bash', category: '未分类', tags: [], currentVersionId: '', currentVersion: 0, draftUpdatedAt: '2026-09-02T08:00:00Z', createdAt: '2026-09-02T08:00:00Z', updatedAt: '2026-09-02T08:00:00Z' },
    ])
    vi.mocked(getScriptSyncs)
      .mockResolvedValueOnce([
        { id: 'sync-old', serverId: 'server-old', serverName: '旧版本节点', scriptId: 'script-1', versionId: 'version-1', versionNumber: 1, state: 'ready', artifactSha256: 'a'.repeat(64), errorCode: '', errorMessage: '', blocked: false, syncedAt: '2026-09-01T08:01:00Z', updatedAt: '2026-09-01T08:01:00Z' },
        { id: 'sync-failed', serverId: 'server-1', serverName: '京东云执行节点-1', scriptId: 'script-1', versionId: 'version-2', versionNumber: 2, state: 'failed', artifactSha256: 'b'.repeat(64), errorCode: 'checksum_mismatch', errorMessage: '脚本包校验失败', blocked: true, syncedAt: null, updatedAt: '2026-09-02T08:01:00Z' },
        { id: 'sync-ready', serverId: 'server-2', serverName: '备用执行节点', scriptId: 'script-1', versionId: 'version-2', versionNumber: 2, state: 'ready', artifactSha256: 'b'.repeat(64), errorCode: '', errorMessage: '', blocked: false, syncedAt: '2026-09-02T08:02:00Z', updatedAt: '2026-09-02T08:02:00Z' },
      ])
      .mockResolvedValueOnce([
        { id: 'sync-failed', serverId: 'server-1', serverName: '京东云执行节点-1', scriptId: 'script-1', versionId: 'version-2', versionNumber: 2, state: 'pending', artifactSha256: 'b'.repeat(64), errorCode: '', errorMessage: '', blocked: false, syncedAt: null, updatedAt: '2026-09-02T08:03:00Z' },
        { id: 'sync-ready', serverId: 'server-2', serverName: '备用执行节点', scriptId: 'script-1', versionId: 'version-2', versionNumber: 2, state: 'ready', artifactSha256: 'b'.repeat(64), errorCode: '', errorMessage: '', blocked: false, syncedAt: '2026-09-02T08:02:00Z', updatedAt: '2026-09-02T08:02:00Z' },
      ])
    vi.mocked(retryScriptSync).mockResolvedValue()
  })

  afterEach(() => cleanup())

  it('汇总当前版本在各服务器的同步状态并允许重试失败节点', async () => {
    const user = userEvent.setup()
    render(<SyncPage />)

    expect(await screen.findByRole('heading', { level: 1, name: '脚本同步' })).toBeVisible()
    const summary = screen.getByRole('region', { name: '同步统计' })
    expect(within(summary).getByText('已发布脚本').parentElement).toHaveTextContent('1')
    expect(within(summary).getByText('已就绪').parentElement).toHaveTextContent('1')
    expect(within(summary).getByText('异常节点').parentElement).toHaveTextContent('1')
    expect(screen.queryByText('旧版本节点')).not.toBeInTheDocument()
    expect(screen.getByRole('row', { name: /订单同步.*京东云执行节点-1.*同步失败/ })).toBeVisible()
    expect(screen.getByText('脚本包校验失败')).toBeVisible()

    await user.click(screen.getByRole('button', { name: '重试订单同步在京东云执行节点-1的同步' }))

    expect(retryScriptSync).toHaveBeenCalledWith('script-1', 'sync-failed')
    expect(await screen.findByText('等待下载')).toBeVisible()
    expect(screen.getByRole('status')).toHaveTextContent('同步请求已重新进入队列')
  })
})
