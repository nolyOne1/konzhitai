import { useCallback, useEffect, useRef, useState } from 'react'

import {
  getBackupSummary, getSession, requestBackup, requestVerification,
  type BackupRun, type BackupSummary, type RestoreVerification,
} from '../../api/client'

interface BackupStatusPanelProps { pollIntervalMs?: number }

export function BackupStatusPanel({ pollIntervalMs = 5000 }: BackupStatusPanelProps) {
  const [summary, setSummary] = useState<BackupSummary | null>(null)
  const [isAdmin, setIsAdmin] = useState(false)
  const [loading, setLoading] = useState(true)
  const [requesting, setRequesting] = useState(false)
  const [message, setMessage] = useState('')
  const [error, setError] = useState('')
  const activeRef = useRef(true)
  const errorRef = useRef<HTMLDivElement>(null)

  const refresh = useCallback(async () => {
    const value = await getBackupSummary()
    if (activeRef.current) setSummary(value)
  }, [])

  useEffect(() => {
    activeRef.current = true
    Promise.all([getBackupSummary(), getSession()])
      .then(([loadedSummary, session]) => {
        if (!activeRef.current) return
        setSummary(loadedSummary)
        setIsAdmin(session.roles.includes('admin'))
      })
      .catch((reason: unknown) => { if (activeRef.current) setError(errorMessage(reason, '读取备份运行状态失败')) })
      .finally(() => { if (activeRef.current) setLoading(false) })
    return () => { activeRef.current = false }
  }, [])

  useEffect(() => {
    if (!summary || !hasActiveWork(summary)) return
    const timer = window.setInterval(() => { void refresh().catch(() => undefined) }, pollIntervalMs)
    return () => window.clearInterval(timer)
  }, [pollIntervalMs, refresh, summary])

  useEffect(() => { if (error) errorRef.current?.focus() }, [error])

  async function enqueueBackup() {
    setRequesting(true); setError(''); setMessage('')
    try {
      await requestBackup()
      if (activeRef.current) { setMessage('备份请求已进入队列'); await refresh() }
    } catch (reason) {
      if (activeRef.current) setError(errorMessage(reason, '创建备份请求失败'))
    } finally { if (activeRef.current) setRequesting(false) }
  }

  async function enqueueVerification() {
    const backupID = summary?.latestCOSBackup?.id
    if (!backupID) return
    setRequesting(true); setError(''); setMessage('')
    try {
      await requestVerification(backupID)
      if (activeRef.current) { setMessage('恢复校验请求已进入队列'); await refresh() }
    } catch (reason) {
      if (activeRef.current) setError(errorMessage(reason, '创建恢复校验请求失败'))
    } finally { if (activeRef.current) setRequesting(false) }
  }

  return (
    <section className="panel backup-status-panel" aria-labelledby="backup-status-title">
      <header className="panel-header">
        <div><h2 id="backup-status-title">运行保障</h2><p>数据库与对象每 6 小时生成本机加密快照，并同步到腾讯云 COS。</p></div>
        <span className={`backup-health is-${summary?.status ?? 'loading'}`}>{loading ? '读取中' : healthLabel(summary?.status)}</span>
      </header>
      <div className="backup-status-body">
        {error ? <div ref={errorRef} className="form-error error-summary" role="alert" tabIndex={-1}>{error}</div> : null}
        {message ? <div className="notice notice-success" role="status">{message}</div> : null}
        <div className="backup-summary-grid">
          <SummaryItem label="下一次自动备份" value={formatDate(summary?.nextBackupAt, '尚未生成计划')} detail="每天 00:30、06:30、12:30、18:30（Asia/Shanghai）" />
          <SummaryItem label="最近本机快照" value={summary?.latestLocalBackup ? backupStatusLabel(summary.latestLocalBackup.status) : '尚无本机快照'} detail={backupDetail(summary?.latestLocalBackup)} />
          <SummaryItem label="最近 COS 快照" value={summary?.latestCOSBackup ? 'COS 已同步' : '尚无 COS 快照'} detail={backupDetail(summary?.latestCOSBackup)} />
          <SummaryItem label="最近恢复校验" value={summary?.latestVerification ? verificationStatusLabel(summary.latestVerification.status) : '尚未执行校验'} detail={verificationDetail(summary?.latestVerification)} />
        </div>
        {isAdmin ? <div className="backup-actions">
          <button className="secondary-action" type="button" onClick={enqueueVerification} disabled={requesting || !summary?.latestCOSBackup}>{requesting ? '正在提交…' : '立即校验'}</button>
          <button className="primary-action" type="button" onClick={enqueueBackup} disabled={requesting}>{requesting ? '正在提交…' : '立即备份'}</button>
        </div> : <p className="backup-readonly-note">当前账号可查看备份与恢复状态；手动请求仅限管理员。</p>}
      </div>
    </section>
  )
}

function SummaryItem({ label, value, detail }: { label: string; value: string; detail: string }) {
  return <div><span>{label}</span><strong>{value}</strong><small>{detail}</small></div>
}

function hasActiveWork(summary: BackupSummary) {
  return summary.status === 'active' || summary.status === 'degraded' || ['queued', 'restoring', 'checking'].includes(summary.latestVerification?.status ?? '')
}

export function backupStatusLabel(status: BackupRun['status']) {
  return ({ queued: '排队中', exporting: '正在导出', snapshotting: '正在生成快照', uploading: '正在上传 COS', succeeded: '成功', degraded: '仅本机成功', failed: '失败' } as const)[status]
}

export function verificationStatusLabel(status: RestoreVerification['status']) {
  return ({ queued: '排队中', restoring: '正在恢复', checking: '正在校验', succeeded: '校验成功', failed: '校验失败' } as const)[status]
}

function healthLabel(status?: BackupSummary['status']) {
  return ({ unavailable: '尚未配置', not_started: '等待首次备份', active: '正在运行', healthy: '运行正常', degraded: '需要关注', failed: '运行异常' } as const)[status ?? 'unavailable']
}

function backupDetail(run?: BackupRun | null) {
  if (!run) return '新安装完成后将在计划时间自动执行。'
  return `${formatBytes(run.byteSize)} · ${run.objectCount} 个对象 · ${formatDate(run.finishedAt ?? run.updatedAt, '时间未知')}`
}

function verificationDetail(item?: RestoreVerification | null) {
  if (!item) return '每月 1 日 03:30 自动从 COS 隔离恢复。'
  return `${item.checkedObjects} 个对象${item.migrationVersion ? ` · 迁移版本 ${item.migrationVersion}` : ''} · ${formatDate(item.finishedAt ?? item.updatedAt, '时间未知')}`
}

function formatDate(value: string | null | undefined, fallback: string) {
  if (!value) return fallback
  return new Intl.DateTimeFormat('zh-CN', { dateStyle: 'short', timeStyle: 'short', hour12: false }).format(new Date(value))
}

function formatBytes(value: number) {
  if (value < 1024) return `${value} B`
  if (value < 1024 ** 2) return `${(value / 1024).toFixed(1)} KiB`
  if (value < 1024 ** 3) return `${(value / 1024 ** 2).toFixed(1)} MiB`
  return `${(value / 1024 ** 3).toFixed(1)} GiB`
}

function errorMessage(reason: unknown, fallback: string) { return reason instanceof Error ? reason.message : fallback }
