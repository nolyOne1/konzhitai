import { useEffect, useMemo, useRef, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'

import { cancelRun, getRun, retryRun, type RunState, type RunView } from '../../api/client'
import { subscribeRunEvents, type RunStreamEvent } from '../../api/events'
import { RunStateBadge } from './RunsPage'

const terminalStates: RunState[] = ['succeeded', 'failed', 'timed_out', 'cancelled', 'expired']

export function RunDetailPage() {
  const { id = '' } = useParams()
  const navigate = useNavigate()
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
  const logRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    let active = true
    getRun(id).then((value) => { if (active) setRun(value) }).catch((reason: unknown) => { if (active) setError(reason instanceof Error ? reason.message : '读取执行详情失败') })
    return () => { active = false }
  }, [id])

  useEffect(() => subscribeRunEvents(id, (event) => {
    setEvents((items) => items.some((item) => item.id === event.id) ? items : [...items, event])
    if (event.kind === 'state' && event.state) setRun((current) => current ? { ...current, state: event.state! } : current)
    setStreamError('')
  }, () => setStreamError('实时连接暂时中断，浏览器会自动重连。')), [id])

  const stateEvents = useMemo(() => events.filter((event) => event.kind === 'state'), [events])
  const allLogs = useMemo(() => events.filter((event) => event.kind === 'log'), [events])
  const logs = useMemo(() => allLogs.slice(clearedCount).filter((event) => !filter.trim() || event.content?.toLocaleLowerCase('zh-CN').includes(filter.trim().toLocaleLowerCase('zh-CN'))), [allLogs, clearedCount, filter])

  useEffect(() => {
    if (!paused && logRef.current) logRef.current.scrollTop = logRef.current.scrollHeight
  }, [logs, paused])

  async function confirmAction() {
    if (!run || !action) return
    setBusy(true)
    setError('')
    try {
      if (action === 'cancel') {
        await cancelRun(run.id)
        setAction(null)
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

  function downloadLogs() {
    const content = allLogs.map((event) => `[${event.occurredAt}] [${event.stream}] ${event.content ?? ''}`).join('')
    const url = URL.createObjectURL(new Blob([content], { type: 'text/plain;charset=utf-8' }))
    const anchor = document.createElement('a')
    anchor.href = url
    anchor.download = `${run?.id ?? 'run'}.log`
    anchor.click()
    URL.revokeObjectURL(url)
  }

  if (error && !run) return <div className="notice notice-error" role="alert">{error}</div>
  if (!run) return <section className="panel run-loading" aria-busy="true">正在读取执行详情…</section>
  const cancellable = ['queued', 'assigned', 'syncing', 'running'].includes(run.state)
  const retryable = run.idempotent && run.processConfirmedGone && run.attempt <= run.maxRetries && ['failed', 'timed_out', 'cancelled', 'unknown'].includes(run.state)

  return (
    <>
      <div className="page-heading run-detail-heading"><div><Link className="back-link" to="/runs">返回执行记录</Link><p className="eyebrow">运行实例 {run.id}</p><h1>{run.taskName}</h1><div className="run-heading-meta"><RunStateBadge state={run.state} /><span>{triggerLabel(run.triggerType)} · 第 {run.attempt} 次执行</span></div></div><div className="run-heading-actions"><button type="button" className="secondary-action" disabled={!retryable} onClick={() => setAction('retry')}>重新执行</button><button type="button" className="danger-action" disabled={!cancellable} onClick={() => setAction('cancel')}>取消任务</button></div></div>
      {error && <div className="notice notice-error" role="alert">{error}</div>}
      <section className="run-context-grid" aria-label="执行上下文">
        <article className="panel run-context-card"><span>执行服务器</span><strong>{run.serverName || '等待自动分配'}</strong><small>{run.serverId || '暂无分配服务器'}</small></article>
        <article className="panel run-context-card"><span>脚本版本</span><strong>{run.scriptName}</strong><small>版本 {run.versionNumber} · {run.requiredRuntime}</small></article>
        <article className="panel run-context-card"><span>资源申请</span><strong>{run.resources.cpuMillicores} 毫核 · {formatBytes(run.resources.memoryBytes)}</strong><small>磁盘 {formatBytes(run.resources.diskBytes)} · 优先级 {run.priority}</small></article>
        <article className="panel run-context-card"><span>执行结果</span><strong>{run.exitCode === undefined ? '尚无退出码' : `退出码 ${run.exitCode}`}</strong><small>{run.finishedAt ? formatDateTime(run.finishedAt) : '任务尚未结束'}</small></article>
      </section>
      <div className="run-detail-grid">
        <section className="panel run-timeline" aria-labelledby="timeline-title"><header className="panel-header"><h2 id="timeline-title">状态时间线</h2><span>{stateEvents.length} 条事件</span></header><ol>{(stateEvents.length ? stateEvents : baseTimeline(run)).map((event) => <li key={event.id}><i aria-hidden="true" /><div><strong>{event.message}</strong><span>{formatDateTime(event.occurredAt)}</span></div></li>)}</ol></section>
        <section className="panel run-parameters" aria-labelledby="parameters-title"><header className="panel-header"><h2 id="parameters-title">参数摘要</h2><span>敏感值不在此显示</span></header><dl>{Object.entries(run.parameters).length ? Object.entries(run.parameters).map(([key, value]) => <div key={key}><dt>{key}</dt><dd>{formatParameter(value)}</dd></div>) : <div><dt>参数</dt><dd>本次执行没有公开参数</dd></div>}</dl></section>
      </div>
      <section className="panel log-panel" aria-labelledby="log-title">
        <header className="log-toolbar"><div><h2 id="log-title">实时日志</h2><span className={streamError ? 'log-connection is-warning' : 'log-connection'}><i aria-hidden="true" />{streamError || (terminalStates.includes(run.state) ? '任务已结束' : '实时连接中')}</span></div><div className="log-actions"><label><span className="sr-only">筛选日志关键词</span><input aria-label="筛选日志关键词" value={filter} onChange={(event) => setFilter(event.target.value)} placeholder="筛选关键词" /></label><button type="button" aria-label={paused ? '继续自动滚动' : '暂停自动滚动'} aria-pressed={paused} onClick={() => setPaused((value) => !value)}>{paused ? '继续滚动' : '暂停滚动'}</button><button type="button" onClick={downloadLogs}>下载日志</button><button type="button" aria-label="清空浏览器显示" onClick={clearDisplay}>清屏显示</button></div></header>
        {cleared && <div className="browser-clear-note" role="status">浏览器显示已清空，服务端日志仍然保留。</div>}
        <div className="log-viewer" ref={logRef} tabIndex={0} aria-label="任务实时日志" aria-live={paused ? 'off' : 'polite'}>{logs.length ? logs.map((event) => <div className={`log-line log-${event.stream}`} key={event.id}><time dateTime={event.occurredAt}>{formatLogTime(event.occurredAt)}</time><span>{event.stream}</span><code>{event.content}</code></div>) : <div className="log-empty">{filter ? '没有匹配当前关键词的日志。' : '等待任务输出日志…'}</div>}</div>
        <footer className="log-status"><span>已接收 {allLogs.length} 个日志块</span><span>{paused ? '自动滚动已暂停' : '自动滚动已开启'}</span></footer>
      </section>
      {action && <div className="drawer-backdrop centered-dialog"><section className="console-dialog" role="dialog" aria-modal="true" aria-labelledby="run-action-title"><header className="drawer-header"><div><p className="eyebrow">{action === 'cancel' ? '停止当前执行' : '创建新的运行实例'}</p><h2 id="run-action-title">{action === 'cancel' ? `确认取消${run.taskName}？` : `确认重新执行${run.taskName}？`}</h2><p>{action === 'cancel' ? '运行中的任务会收到终止命令；排队任务会直接取消。' : '只有原进程已确认结束且任务允许幂等重试时才会进入队列。'}</p></div></header><footer className="dialog-actions"><button type="button" className="secondary-action" onClick={() => setAction(null)}>返回</button><button type="button" className={action === 'cancel' ? 'danger-action' : 'primary-action'} disabled={busy} onClick={() => void confirmAction()}>{busy ? '处理中…' : action === 'cancel' ? '确认取消' : '确认重试'}</button></footer></section></div>}
    </>
  )
}

function baseTimeline(run: RunView): RunStreamEvent[] { return [{ id: 'base-queued', kind: 'state', state: 'queued', sequence: 0, message: '任务已进入排队队列', occurredAt: run.queuedAt }] }
function triggerLabel(trigger: RunView['triggerType']) { return trigger === 'manual' ? '手动执行' : trigger === 'schedule' ? '定时计划' : '失败重试' }
function formatBytes(value: number) { return value >= 1073741824 ? `${(value / 1073741824).toFixed(1)} GB` : `${Math.round(value / 1048576)} MB` }
function formatDateTime(value: string) { return new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'medium', hour12: false }).format(new Date(value)) }
function formatLogTime(value: string) { return new Intl.DateTimeFormat('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit', fractionalSecondDigits: 3, hour12: false }).format(new Date(value)) }
function formatParameter(value: unknown) { return typeof value === 'string' ? value : JSON.stringify(value) }
