import { render, screen } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { getBackups, getRestoreVerifications } from '../../api/client'
import { BackupHistoryPanel } from './BackupHistoryPanel'

vi.mock('../../api/client', () => ({ getBackups: vi.fn(), getRestoreVerifications: vi.fn() }))

describe('备份与恢复历史', () => {
  beforeEach(() => {
    vi.mocked(getBackups).mockResolvedValue([
      { id: 'backup-1', triggerType: 'scheduled', status: 'uploading', localSnapshotId: 'local', byteSize: 2048, objectCount: 3, attempts: 1, nextAttemptAt: '2026-09-01T12:30:00Z', createdAt: '2026-09-01T12:30:00Z', updatedAt: '2026-09-01T12:31:00Z' },
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
})
