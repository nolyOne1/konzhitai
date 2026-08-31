import { useCallback, useEffect, useRef, useState } from 'react'

import { getBackups, getRestoreVerifications, type BackupRun, type RestoreVerification } from '../../api/client'
import { backupStatusLabel, verificationStatusLabel } from './BackupStatusPanel'

interface BackupHistoryPanelProps { pollIntervalMs?: number }

export function BackupHistoryPanel({ pollIntervalMs = 5000 }: BackupHistoryPanelProps) {
  const [backups, setBackups] = useState<BackupRun[]>([])
  const [verifications, setVerifications] = useState<RestoreVerification[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const activeRef = useRef(true)
  const errorRef = useRef<HTMLDivElement>(null)
  const refresh = useCallback(async () => {
    const [nextBackups, nextVerifications] = await Promise.all([getBackups(), getRestoreVerifications()])
    if (activeRef.current) { setBackups(nextBackups); setVerifications(nextVerifications) }
  }, [])

  useEffect(() => {
    activeRef.current = true
    refresh().catch((reason: unknown) => { if (activeRef.current) setError(reason instanceof Error ? reason.message : '读取备份历史失败') })
      .finally(() => { if (activeRef.current) setLoading(false) })
    return () => { activeRef.current = false }
  }, [refresh])

  useEffect(() => {
    const active = backups.some((item) => ['queued', 'exporting', 'snapshotting', 'uploading', 'degraded'].includes(item.status)) ||
      verifications.some((item) => ['queued', 'restoring', 'checking'].includes(item.status))
    if (!active) return
    const timer = window.setInterval(() => { void refresh().catch(() => undefined) }, pollIntervalMs)
    return () => window.clearInterval(timer)
  }, [backups, pollIntervalMs, refresh, verifications])

  useEffect(() => { if (error) errorRef.current?.focus() }, [error])

  return <div className="backup-history-grid">
    {error ? <div ref={errorRef} className="form-error error-summary backup-history-error" role="alert" tabIndex={-1}>{error}</div> : null}
    <section className="panel backup-history-panel" aria-labelledby="backup-history-title">
      <header className="panel-header"><div><h2 id="backup-history-title">备份历史</h2><p>本机保留 7 天，COS 保留 30 天；降级记录会从本机快照续传。</p></div></header>
      {loading ? <Loading text="正在读取备份历史…" /> : backups.length === 0 ? <Empty text="尚无备份记录" /> : <div className="table-scroll"><table className="data-table"><thead><tr><th>创建时间</th><th>触发方式</th><th>状态</th><th>范围</th><th>尝试</th></tr></thead><tbody>{backups.map((run) => <tr key={run.id}><td data-label="创建时间">{formatTime(run.createdAt)}</td><td data-label="触发方式">{run.triggerType === 'manual' ? '手动' : '定时'}</td><td data-label="状态"><Status value={backupStatusLabel(run.status)} failed={run.status === 'failed'} degraded={run.status === 'degraded'} /></td><td data-label="范围">{formatBytes(run.byteSize)} · {run.objectCount} 个对象</td><td data-label="尝试">{run.attempts}</td></tr>)}</tbody></table></div>}
    </section>
    <section className="panel backup-history-panel" aria-labelledby="verification-history-title">
      <header className="panel-header"><div><h2 id="verification-history-title">恢复校验历史</h2><p>只恢复到随机临时数据库和隔离目录，不覆盖生产数据。</p></div></header>
      {loading ? <Loading text="正在读取恢复校验历史…" /> : verifications.length === 0 ? <Empty text="尚无恢复校验记录" /> : <div className="table-scroll"><table className="data-table"><thead><tr><th>创建时间</th><th>触发方式</th><th>状态</th><th>校验结果</th></tr></thead><tbody>{verifications.map((item) => <tr key={item.id}><td data-label="创建时间">{formatTime(item.createdAt)}</td><td data-label="触发方式">{item.triggerType === 'manual' ? '手动' : '定时'}</td><td data-label="状态"><Status value={verificationStatusLabel(item.status)} failed={item.status === 'failed'} /></td><td data-label="校验结果"><span>{item.migrationVersion ? `迁移版本 ${item.migrationVersion}` : '等待结果'}</span><small className="cell-note">{item.checkedObjects} 个对象</small></td></tr>)}</tbody></table></div>}
    </section>
  </div>
}

function Status({ value, failed = false, degraded = false }: { value: string; failed?: boolean; degraded?: boolean }) { return <span className={`backup-status-chip${failed ? ' is-failed' : degraded ? ' is-degraded' : ''}`}>{value}</span> }
function Loading({ text }: { text: string }) { return <div className="compact-empty" aria-live="polite"><span aria-hidden="true" />{text}</div> }
function Empty({ text }: { text: string }) { return <div className="backup-empty"><strong>{text}</strong><p>Ops 服务运行后会自动在这里追加记录。</p></div> }
function formatTime(value: string) { return new Intl.DateTimeFormat('zh-CN', { dateStyle: 'short', timeStyle: 'short', hour12: false }).format(new Date(value)) }
function formatBytes(value: number) { return value < 1024 ? `${value} B` : value < 1024 ** 2 ? `${(value / 1024).toFixed(1)} KiB` : value < 1024 ** 3 ? `${(value / 1024 ** 2).toFixed(1)} MiB` : `${(value / 1024 ** 3).toFixed(1)} GiB` }
