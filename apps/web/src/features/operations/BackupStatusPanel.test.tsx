import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { getBackupSummary, getSession, requestBackup, requestVerification } from '../../api/client'
import { BackupStatusPanel } from './BackupStatusPanel'

vi.mock('../../api/client', () => ({
  getBackupSummary: vi.fn(), getSession: vi.fn(), requestBackup: vi.fn(), requestVerification: vi.fn(),
}))

describe('备份运行保障面板', () => {
  beforeEach(() => {
    vi.mocked(getSession).mockResolvedValue({ id: 'admin-1', displayName: '管理员', email: 'admin@example.com', roles: ['admin'] })
    vi.mocked(getBackupSummary).mockResolvedValue({
      status: 'not_started', nextBackupAt: '2026-09-01T16:30:00Z', latestLocalBackup: null,
      latestCOSBackup: null, latestVerification: null,
    })
    vi.mocked(requestBackup).mockResolvedValue({ id: 'backup-1', triggerType: 'manual', status: 'queued', byteSize: 0, objectCount: 0, attempts: 0, nextAttemptAt: '2026-09-01T12:00:00Z', createdAt: '2026-09-01T12:00:00Z', updatedAt: '2026-09-01T12:00:00Z' })
  })

  it('显示首次未备份空状态并允许管理员立即备份', async () => {
    const user = userEvent.setup()
    render(<BackupStatusPanel pollIntervalMs={5} />)

    expect(await screen.findByText('下一次自动备份')).toBeVisible()
    expect(screen.getByText('尚无本机快照')).toBeVisible()
    await user.click(screen.getByRole('button', { name: '立即备份' }))
    expect(await screen.findByText('备份请求已进入队列')).toBeVisible()
    expect(requestBackup).toHaveBeenCalledOnce()
  })

  it('显示仅本机成功与最近校验失败并允许立即校验', async () => {
    vi.mocked(getBackupSummary).mockResolvedValue({
      status: 'degraded', nextBackupAt: null,
      latestLocalBackup: { id: 'backup-1', triggerType: 'scheduled', status: 'degraded', localSnapshotId: 'local', byteSize: 1024, objectCount: 2, attempts: 2, nextAttemptAt: '2026-09-01T12:15:00Z', createdAt: '2026-09-01T12:00:00Z', updatedAt: '2026-09-01T12:01:00Z' },
      latestCOSBackup: { id: 'backup-0', triggerType: 'scheduled', status: 'succeeded', localSnapshotId: 'local-0', cosSnapshotId: 'cos-0', byteSize: 1024, objectCount: 2, attempts: 1, nextAttemptAt: '2026-09-01T06:30:00Z', createdAt: '2026-09-01T06:30:00Z', updatedAt: '2026-09-01T06:31:00Z' },
      latestVerification: { id: 'verify-1', backupRunId: 'backup-0', triggerType: 'scheduled', status: 'failed', checkedObjects: 0, createdAt: '2026-09-01T07:00:00Z', updatedAt: '2026-09-01T07:01:00Z' },
    })
    vi.mocked(requestVerification).mockResolvedValue({ id: 'verify-2', backupRunId: 'backup-0', triggerType: 'manual', status: 'queued', checkedObjects: 0, createdAt: '2026-09-01T12:00:00Z', updatedAt: '2026-09-01T12:00:00Z' })
    const user = userEvent.setup()
    render(<BackupStatusPanel pollIntervalMs={5} />)

    expect(await screen.findByText('仅本机成功')).toBeVisible()
    expect(screen.getByText('校验失败')).toBeVisible()
    await user.click(screen.getByRole('button', { name: '立即校验' }))
    expect(await screen.findByText('恢复校验请求已进入队列')).toBeVisible()
    expect(requestVerification).toHaveBeenCalledWith('backup-0')
  })

  it('viewer 看不到写操作按钮', async () => {
    vi.mocked(getSession).mockResolvedValue({ id: 'viewer-1', displayName: '观察员', email: 'viewer@example.com', roles: ['viewer'] })
    render(<BackupStatusPanel pollIntervalMs={5} />)
    expect(await screen.findByText('下一次自动备份')).toBeVisible()
    expect(screen.queryByRole('button', { name: '立即备份' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '立即校验' })).not.toBeInTheDocument()
  })
})
