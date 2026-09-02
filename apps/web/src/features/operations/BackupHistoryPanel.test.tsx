import { render, screen, within } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { getBackups, getRestoreVerifications } from '../../api/client'
import { BackupHistoryPanel } from './BackupHistoryPanel'

vi.mock('../../api/client', () => ({ getBackups: vi.fn(), getRestoreVerifications: vi.fn() }))

describe('备份与恢复历史', () => {
  beforeEach(() => {
    vi.mocked(getBackups).mockResolvedValue([
      { id: 'backup-1', triggerType: 'scheduled', status: 'queued', scheduledFor: '2026-09-10T12:30:00Z', byteSize: 0, objectCount: 0, attempts: 0, nextAttemptAt: '2026-09-10T12:30:00Z', createdAt: '2026-08-01T12:30:00Z', updatedAt: '2026-08-01T12:30:00Z' },
      { id: 'backup-2', triggerType: 'manual', status: 'uploading', localSnapshotId: 'local', byteSize: 2048, objectCount: 3, attempts: 1, nextAttemptAt: '2026-09-02T12:30:00Z', createdAt: '2026-09-02T12:30:00Z', updatedAt: '2026-09-02T12:31:00Z' },
    ])
    vi.mocked(getRestoreVerifications).mockResolvedValue([
      { id: 'verify-1', backupRunId: 'backup-0', triggerType: 'manual', status: 'succeeded', migrationVersion: '12', checkedObjects: 3, createdAt: '2026-09-01T11:00:00Z', updatedAt: '2026-09-01T11:03:00Z' },
    ])
  })

  it('以中文展示备份和恢复校验状态', async () => {
    render(<BackupHistoryPanel pollIntervalMs={5} />)
    expect(await screen.findByRole('heading', { name: '备份历史' })).toBeVisible()
    expect(screen.getByText('正在上传 COS')).toBeVisible()
    expect(screen.getByRole('heading', { name: '恢复校验历史' })).toBeVisible()
    expect(screen.getByText('校验成功')).toBeVisible()
    expect(screen.getByText('迁移版本 12')).toBeVisible()
  })

  it('定时备份显示计划时间而不是占位记录的创建时间', async () => {
    render(<BackupHistoryPanel pollIntervalMs={5} />)

    const scheduledRow = await screen.findByRole('row', { name: /定时.*排队中/ })
    expect(within(scheduledRow).getByText('计划时间')).toBeVisible()
    expect(scheduledRow).toHaveTextContent('2026/9/10')
    expect(scheduledRow).not.toHaveTextContent('2026/8/1')

    const manualRow = screen.getByRole('row', { name: /手动.*正在上传 COS/ })
    expect(within(manualRow).getByText('创建时间')).toBeVisible()
    expect(manualRow).toHaveTextContent('2026/9/2')
  })
})
