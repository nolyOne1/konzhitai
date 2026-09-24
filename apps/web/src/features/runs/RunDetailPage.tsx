import { useEffect, useMemo, useRef, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'

import { cancelRun, downloadRunArtifact, downloadRunLogs, getRun, getRunArtifacts, getRunLogArchiveInfo, getTask, retryRun, type RunArtifact, type RunLogArchiveInfo, type RunState, type RunView, type TaskDefinition } from '../../api/client'
import { subscribeRunEvents, type RunStreamEvent } from '../../api/events'
import { RunStateBadge } from './RunsPage'
import { useCanExecute } from '../auth/SessionContext'
import { ManualRunDialog } from '../tasks/ManualRunDialog'

const terminalStates: RunState[] = ['succeeded', 'failed', 'timed_out', 'cancelled', 'expired']

export function RunDetailPage() {
  const { id = '' } = useParams()
  const navigate = useNavigate()
  const canExecute = useCanExecute()
  const [manualTask, setManualTask] = useState<TaskDefinition | null>(null)
  const [run, setRun] = useState<RunView | null>(null)
  const [events, setEvents] = useState<RunStreamEvent[]>([])
  const [error, setError] = useState('')
  const [streamError, setStreamError] = useState('')
  const [filter, setFilter] = useState('')
  const [paused, setPaused] = useState(false)
  const [clearedCount, setClearedCount] = useState(0)
  const [cleared, setCleared] = useState(false)
  const [action, setAction] = useState<'cancel' | 'retry' | null>(null)
  const [busy, setBusy] = useState(false)
  const [downloading, setDownloading] = useState(false)
  const [status, setStatus] = useState('')
  const [archive, setArchive] = useState<RunLogArchiveInfo | null>(null)
  const [archiveError, setArchiveError] = useState('')
  const [artifacts, setArtifacts] = useState<RunArtifact[]>([])
  const [artifactError, setArtifactError] = useState('')
  const [downloadingArtifact, setDownloadingArtifact] = useState('')
  const logRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    let active = true
    let request = 0
    const seen = new Set<string>()
    setRun(null)
    setEvents([])
    setError('')
    setStreamError('')
    setStatus('')
    setClearedCount(0)
    setCleared(false)
    async function refresh() {
      const currentRequest = ++request
      try {
        const value = await getRun(id)
        if (active && currentRequest === request) { setRun(value); setError('') }
      } catch (reason) {
        if (active && currentRequest === request) setError(reason instanceof Error ? reason.message : '读取执行详情失败')
      }
    }
    void refresh()
    const unsubscribe = subscribeRunEvents(id, (event) => {
      if (!active || seen.has(event.id)) return
      seen.add(event.id)
      setEvents((items) => [...items, event])
      if (event.usage) {
        setRun((current) => current && (!current.usage || new Date(event.usage!.sampledAt) >= new Date(current.usage.sampledAt)) ? { ...current, usage: event.usage } : current)
      }
      // State events do not contain the complete execution context. Reload the
      // authoritative snapshot, ignoring responses superseded by later events.
      if (event.kind === 'state' && event.state && event.eventType !== 'run.usage') {
        setRun((current) => current ? { ...current, state: event.state! } : current)
        void refresh()
      }
      setStreamError('')
    }, () => { if (active) setStreamError('实时连接暂时中断，浏览器会自动重连。') })
    return () => { active = false; unsubscribe() }
  }, [id])

  const runState = run?.state
  useEffect(() => {
    setArchive(null)
    setArchiveError('')
    setArtifacts([])
    setArtifactError('')
    if (!runState || !terminalStates.includes(runState)) return
    let active = true
    async function refreshArchive() {
      const [archiveResult, artifactResult] = await Promise.allSettled([getRunLogArchiveInfo(id), getRunArtifacts(id)])
      if (!active) return
      if (archiveResult.status === 'fulfilled') { setArchive(archiveResult.value); setArchiveError('') }
      else setArchiveError('暂时无法读取归档状态，仍可下载完整日志。')
      if (artifactResult.status === 'fulfilled') { setArtifacts(artifactResult.value ?? []); setArtifactError('') }
      else setArtifactError('暂时无法读取运行产物，稍后将自动重试。')
    }
    void refreshArchive()
    const timer = window.setInterval(() => { if (document.visibilityState !== 'hidden') void refreshArchive() }, 15000)
    return () => { active = false; window.clearInterval(timer) }
  }, [id, runState])

  const stateEvents = useMemo(() => events.filter((event) => event.kind === 'state' && event.eventType !== 'run.usage'), [events])
  const allLogs = useMemo(() => events.filter((event) => event.kind === 'log'), [events])
  const logs = useMemo(() => allLogs.slice(clearedCount).filter((event) => !filter.trim() || event.content?.toLocaleLowerCase('zh-CN').includes(filter.trim().toLocaleLowerCase('zh-CN'))), [allLogs, clearedCount, filter])

  useEffect(() => {
    if (!paused && logRef.current) logRef.current.scrollTop = logRef.current.scrollHeight
  }, [logs, paused])

  async function confirmAction() {
    if (!run || !action || !canExecute) return
    setBusy(true)
    setError('')
    try {
      if (action === 'cancel') {
        await cancelRun(run.id)
        setAction(null)
        setStatus(run.state === 'queued' ? '排队任务已取消。' : '取消命令已发送，正在等待执行服务器确认。')
      } else {
        const nextID = await retryRun(run.id)
        navigate(`/runs/${encodeURIComponent(nextID)}`)
      }
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '操作失败')
    } finally {
      setBusy(false)
    }
  }

  function clearDisplay() {
    setClearedCount(allLogs.length)
    setCleared(true)
  }

  async function prepareNewRun() {
    if (!run || !canExecute || !terminalStates.includes(run.state)) return
    setBusy(true); setError('')
    try { setManualTask(await getTask(run.definitionId)) }
    catch (reason) { setError(reason instanceof Error ? reason.message : '读取当前任务配置失败') }
    finally { setBusy(false) }
  }

  async function downloadLogs(compressed = false) {
    setDownloading(true); setError('')
    try {
      const content = await downloadRunLogs(id, compressed)
      const url = URL.createObjectURL(content)
      const anchor = document.createElement('a')
      anchor.href = url
      anchor.download = compressed ? `${id}.logs.ndjson.gz` : `${id}.log`
      anchor.click()
      URL.revokeObjectURL(url)
      setStatus(compressed ? '日志压缩归档已下载。' : '已下载服务端截至当前保留的完整日志。')
    } catch (reason) { setError(reason instanceof Error ? reason.message : '下载日志失败') }
    finally { setDownloading(false) }
  }

  async function downloadArtifact(artifact: RunArtifact) {
    setDownloadingArtifact(artifact.id); setArtifactError('')
    try {
      const body = await downloadRunArtifact(id, artifact.id)
      const url = URL.createObjectURL(body)
      const anchor = document.createElement('a')
      anchor.href = url; anchor.download = artifact.name; anchor.click()
      URL.revokeObjectURL(url)
      setStatus(`已下载运行产物：${artifact.name}`)
    } catch (reason) { setArtifactError(reason instanceof Error ? reason.message : '下载运行产物失败') }
    finally { setDownloadingArtifact('') }
  }

  if (error && !run) return <div className="notice notice-error" role="alert">{error}</div>
  if (!run) return <section className="panel run-loading" aria-busy="true">正在读取执行详情…</section>
  const cancellable = ['queued', 'assigned', 'syncing', 'running'].includes(run.state)
  const retryable = run.idempotent && run.processConfirmedGone && run.attempt <= run.maxRetries && ['failed', 'timed_out', 'cancelled', 'unknown'].includes(run.state)

  return (
    <>
      <div className="page-heading run-detail-heading"><div><Link className="back-link" to="/runs">返回执行记录</Link><p className="eyebrow">运行实例 {run.id}</p><h1>{run.taskName}</h1><div className="run-heading-meta"><RunStateBadge state={run.state} /><span>{triggerLabel(run.triggerType)} · 第 {run.attempt} 次执行</span></div></div><div className="run-heading-actions"><button type="button" className="secondary-action" disabled={!canExecute || busy || !terminalStates.includes(run.state)} onClick={() => void prepareNewRun()}>按当前任务配置再次执行</button><button type="button" className="secondary-action" disabled={!canExecute || busy || !retryable} onClick={() => setAction('retry')}>安全重试</button><button type="button" className="danger-action" disabled={!canExecute || busy || !cancellable} onClick={() => setAction('cancel')}>取消任务</button></div></div>
      {error && <div className="notice notice-error" role="alert">{error}</div>}
      {status && <div className="notice notice-success" role="status">{status}</div>}
      {!canExecute && <div className="notice" role="status">当前角色仅可查看执行状态与日志。</div>}
      <div className="row-actions run-audit-links"><Link className="secondary-action button-link" to={`/settings?targetType=run&targetId=${encodeURIComponent(run.id)}`}>本次运行审计</Link><Link className="secondary-action button-link" to={`/settings?targetType=task&targetId=${encodeURIComponent(run.definitionId)}`}>关联任务审计</Link><Link className="secondary-action button-link" to={`/tasks/${encodeURIComponent(run.definitionId)}`}>查看任务配置</Link></div>
      {run.resultSummary && <div className="notice"><strong>执行说明：</strong>{run.resultSummary}</div>}
      <section className="run-context-grid" aria-label="执行上下文">
        <article className="panel run-context-card"><span>执行服务器</span><strong>{run.serverName || '等待自动分配'}</strong><small>{run.serverId || '暂无分配服务器'}</small></article>
        <article className="panel run-context-card"><span>脚本版本</span><strong>{run.scriptName}</strong><small>版本 {run.versionNumber} · {run.requiredRuntime}</small></article>
        <article className="panel run-context-card"><span>资源申请</span><strong>{run.resources.cpuMillicores} 毫核 · {formatBytes(run.resources.memoryBytes)}</strong><small>磁盘 {formatBytes(run.resources.diskBytes)} · 优先级 {run.priority}</small></article>
        <article className="panel run-context-card"><span>执行结果</span><strong>{run.state === 'timed_out' ? '执行超时' : run.state === 'cancelled' ? '任务已取消' : run.exitCode === undefined ? '尚无退出码' : run.exitCode === -1 ? '未取得正常退出码' : `退出码 ${run.exitCode}`}</strong><small>{run.finishedAt ? formatDateTime(run.finishedAt) : '任务尚未结束'}</small></article>
      </section>
      <div className="run-detail-grid">
        <section className="panel run-usage-panel" aria-labelledby="usage-title"><header className="panel-header"><div><h2 id="usage-title">实际资源使用</h2><p>{run.usage ? `最近采样：${formatDateTime(run.usage.sampledAt)}` : '执行代理尚未上报资源用量。'}</p></div></header><dl className="run-usage-grid"><div><dt>累计 CPU 时间</dt><dd>{run.usage?.cpuTimeMillis === undefined ? '—' : `${(run.usage.cpuTimeMillis / 1000).toFixed(2)} 秒`}</dd></div><div><dt>采样内存</dt><dd>{run.usage?.memoryBytes === undefined ? '—' : formatBytes(run.usage.memoryBytes)}</dd></div><div><dt>观测内存峰值</dt><dd>{run.usage?.peakMemoryBytes === undefined ? '—' : formatBytes(run.usage.peakMemoryBytes)}</dd></div><div><dt>进程数</dt><dd>{run.usage?.processes ?? '—'}</dd></div></dl></section>
        <section className="panel run-timeline" aria-labelledby="timeline-title"><header className="panel-header"><h2 id="timeline-title">状态时间线</h2><span>{stateEvents.length} 条事件</span></header><ol>{(stateEvents.length ? stateEvents : baseTimeline(run)).map((event) => <li key={event.id}><i aria-hidden="true" /><div><strong>{event.message}</strong><span>{formatDateTime(event.occurredAt)}</span></div></li>)}</ol></section>
        <section className="panel run-parameters" aria-labelledby="parameters-title"><header className="panel-header"><h2 id="parameters-title">参数摘要</h2><span>敏感值不在此显示</span></header><dl>{Object.entries(run.parameters).length ? Object.entries(run.parameters).map(([key, value]) => <div key={key}><dt>{key}</dt><dd>{formatParameter(value)}</dd></div>) : <div><dt>参数</dt><dd>本次执行没有公开参数</dd></div>}</dl></section>
      </div>
      <section className="panel log-panel" aria-labelledby="log-title">
        <header className="log-toolbar"><div><h2 id="log-title">实时日志</h2><span className={streamError ? 'log-connection is-warning' : 'log-connection'}><i aria-hidden="true" />{streamError || (terminalStates.includes(run.state) ? '任务已结束' : '实时连接中')}</span></div><div className="log-actions"><label><span className="sr-only">筛选日志关键词</span><input aria-label="筛选日志关键词" value={filter} onChange={(event) => setFilter(event.target.value)} placeholder="筛选关键词" /></label><button type="button" aria-label={paused ? '继续自动滚动' : '暂停自动滚动'} aria-pressed={paused} onClick={() => setPaused((value) => !value)}>{paused ? '继续滚动' : '暂停滚动'}</button><button type="button" disabled={downloading} onClick={() => void downloadLogs()}>{downloading ? '正在下载…' : '下载日志'}</button><button type="button" aria-label="清空浏览器显示" onClick={clearDisplay}>清屏显示</button></div></header>
        {cleared && <div className="browser-clear-note" role="status">浏览器显示已清空，服务端日志仍然保留。</div>}
        <div className="log-viewer" ref={logRef} tabIndex={0} aria-label="任务实时日志" aria-live={paused ? 'off' : 'polite'}>{logs.length ? logs.map((event) => <div className={`log-line log-${event.stream}`} key={event.id}><time dateTime={event.occurredAt}>{formatLogTime(event.occurredAt)}</time><span>{event.stream}</span><code>{event.content}</code></div>) : <div className="log-empty">{filter ? '没有匹配当前关键词的日志。' : '等待任务输出日志…'}</div>}</div>
        <footer className="log-status"><span>已接收 {allLogs.length} 个日志块</span><span>{paused ? '自动滚动已暂停' : '自动滚动已开启'}</span></footer>
        {(archive?.available || archiveError) && <div className="log-status log-archive-status"><span>{archiveError || (archive?.current ? `压缩归档已就绪${archive.byteSize ? ` · ${formatBytes(archive.byteSize)}` : ''}` : '归档正在更新，请使用完整日志下载。')}</span>{archive?.available && archive.current && <button type="button" className="secondary-action" disabled={downloading} onClick={() => void downloadLogs(true)}>下载压缩归档</button>}</div>}
      </section>
      {action && <div className="drawer-backdrop centered-dialog"><section className="console-dialog" role="dialog" aria-modal="true" aria-labelledby="run-action-title"><header className="drawer-header"><div><p className="eyebrow">{action === 'cancel' ? '停止当前执行' : '创建新的运行实例'}</p><h2 id="run-action-title">{action === 'cancel' ? `确认取消${run.taskName}？` : `确认重新执行${run.taskName}？`}</h2><p>{action === 'cancel' ? '运行中的任务会收到终止命令；排队任务会直接取消。' : '只有原进程已确认结束且任务允许幂等重试时才会进入队列。'}</p></div></header><footer className="dialog-actions"><button type="button" className="secondary-action" onClick={() => setAction(null)}>返回</button><button type="button" className={action === 'cancel' ? 'danger-action' : 'primary-action'} disabled={busy} onClick={() => void confirmAction()}>{busy ? '处理中…' : action === 'cancel' ? '确认取消' : '确认重试'}</button></footer></section></div>}
      <section className="panel run-artifacts-panel" aria-labelledby="artifacts-title"><header className="panel-header"><div><h2 id="artifacts-title">运行产物</h2><p>脚本启用产物采集后，执行结束时上传的文件会显示在这里。</p></div><span>{artifacts.length} 个文件</span></header>{artifactError && <div className="notice notice-error" role="alert">{artifactError}</div>}{artifacts.length === 0 ? <p className="compact-empty">暂无可下载产物。</p> : <div className="table-scroll"><table className="data-table"><thead><tr><th>文件</th><th>大小</th><th>上传时间</th><th>操作</th></tr></thead><tbody>{artifacts.map((artifact) => <tr key={artifact.id}><td><strong>{artifact.name}</strong><small className="cell-note">SHA-256：{artifact.sha256}</small></td><td>{formatFileSize(artifact.byteSize)}</td><td>{formatDateTime(artifact.createdAt)}</td><td><button type="button" className="secondary-action" disabled={Boolean(downloadingArtifact)} onClick={() => void downloadArtifact(artifact)}>{downloadingArtifact === artifact.id ? '正在下载…' : `下载 ${artifact.name}`}</button></td></tr>)}</tbody></table></div>}</section>
      {manualTask && <ManualRunDialog task={manualTask} rerun onClose={() => setManualTask(null)} onStarted={(next) => { setManualTask(null); navigate(`/runs/${encodeURIComponent(next.id)}`) }} />}
    </>
  )
}

function baseTimeline(run: RunView): RunStreamEvent[] { return [{ id: 'base-queued', kind: 'state', state: 'queued', sequence: 0, message: '任务已进入排队队列', occurredAt: run.queuedAt }] }
function triggerLabel(trigger: RunView['triggerType']) { return trigger === 'manual' ? '手动执行' : trigger === 'schedule' ? '定时计划' : '失败重试' }
function formatBytes(value: number) { return value >= 1073741824 ? `${(value / 1073741824).toFixed(1)} GB` : `${Math.round(value / 1048576)} MB` }
function formatFileSize(value: number) { return value < 1024 ? `${value} B` : value < 1048576 ? `${(value / 1024).toFixed(1)} KB` : formatBytes(value) }
function formatDateTime(value: string) { return new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'medium', hour12: false }).format(new Date(value)) }
function formatLogTime(value: string) { return new Intl.DateTimeFormat('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit', fractionalSecondDigits: 3, hour12: false }).format(new Date(value)) }
function formatParameter(value: unknown) { return typeof value === 'string' ? value : JSON.stringify(value) }
