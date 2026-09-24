import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'

import { getRunPage, getTasks, getScripts, getServers, type RunFilter, type RunState, type RunView } from '../../api/client'

const stateLabels: Record<RunState, string> = {
  queued: '排队等待', scheduling: '正在调度', assigned: '已分配', syncing: '同步脚本', running: '运行中',
  succeeded: '执行成功', failed: '执行失败', timed_out: '执行超时', cancelled: '已取消', expired: '排队过期', unknown: '待确认',
}

export function RunsPage() {
  const [runs, setRuns] = useState<RunView[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [query, setQuery] = useState('')
  const [state, setState] = useState<RunState | 'all'>('all')
  const [taskId, setTaskId] = useState('')
  const [scriptId, setScriptId] = useState('')
  const [serverId, setServerId] = useState('')
  const [from, setFrom] = useState('')
  const [until, setUntil] = useState('')
  const [filter, setFilter] = useState<RunFilter>({ limit: 50, offset: 0 })
  const [hasMore, setHasMore] = useState(false)
  const [refresh, setRefresh] = useState(0)
  const [choices, setChoices] = useState<{ tasks: { id: string; name: string }[]; scripts: { id: string; name: string }[]; servers: { id: string; name: string }[] }>({ tasks: [], scripts: [], servers: [] })
  const [filterError, setFilterError] = useState('')

  useEffect(() => {
    let active = true
    Promise.all([getTasks(), getScripts(), getServers()])
      .then(([tasks, scripts, servers]) => { if (active) setChoices({ tasks: tasks ?? [], scripts: scripts ?? [], servers: servers ?? [] }) })
      .catch(() => { if (active) setFilterError('筛选选项加载失败，仍可使用关键词、状态和时间检索。') })
    return () => { active = false }
  }, [])

  useEffect(() => {
    let active = true
    let request = 0
    setLoading(true)
    async function load() {
      const current = ++request
      try {
        const page = await getRunPage(filter)
        if (active && current === request) { setRuns(page.runs); setHasMore(Boolean(page.hasMore)); setError('') }
      } catch (reason) { if (active && current === request) setError(reason instanceof Error ? reason.message : '读取执行记录失败') }
      finally { if (active && current === request) setLoading(false) }
    }
    void load()
    const timer = window.setInterval(() => { if (document.visibilityState !== 'hidden') void load() }, 10000)
    return () => { active = false; window.clearInterval(timer) }
  }, [filter, refresh])

  function search(event: React.FormEvent) {
    event.preventDefault()
    if (from && until && new Date(from) >= new Date(until)) { setError('结束时间必须晚于开始时间。'); return }
    setFilter({ query: query.trim(), state: state === 'all' ? undefined : state, taskId, scriptId, serverId,
      from: from ? new Date(from).toISOString() : undefined, until: until ? new Date(until).toISOString() : undefined, limit: 50, offset: 0 })
  }

  return (
    <>
      <div className="page-heading">
        <div><p className="eyebrow">运行历史与当前状态</p><h1>执行记录</h1><p>排队是任务的运行状态；没有合适服务器时继续等待，资源恢复后自动调度。</p></div>
      </div>
      {error && <div className="notice notice-error" role="alert">{error}</div>}
      <section className="run-summary" aria-label="执行概况">
        <div><span>本页运行中</span><strong>{loading ? '—' : runs.filter((run) => run.state === 'running').length}</strong></div>
        <div><span>本页排队</span><strong>{loading ? '—' : runs.filter((run) => run.state === 'queued').length}</strong></div>
        <div><span>本页待确认</span><strong>{loading ? '—' : runs.filter((run) => run.state === 'unknown').length}</strong></div>
        <div><span>本页记录</span><strong>{loading ? '—' : runs.length}</strong></div>
      </section>
      <section className="panel run-list-panel" aria-labelledby="run-list-title" aria-busy={loading}>
        <header className="panel-header"><div><h2 id="run-list-title">运行实例</h2><p>检索全部历史记录，页面每 10 秒刷新；时间按浏览器所在时区填写。</p></div><button type="button" className="secondary-action" disabled={loading} onClick={() => setRefresh((value) => value + 1)}>刷新记录</button></header>
        {filterError && <div className="notice" role="status">{filterError}</div>}
          <form className="run-filters run-history-filters" onSubmit={search}>
            <label><span className="sr-only">筛选执行记录</span><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索执行记录" /></label>
            <label><span className="sr-only">筛选运行状态</span><select value={state} onChange={(event) => setState(event.target.value as RunState | 'all')}><option value="all">全部状态</option>{Object.entries(stateLabels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></label>
            <label><span className="sr-only">筛选任务</span><select value={taskId} onChange={(event) => setTaskId(event.target.value)}><option value="">全部任务</option>{choices.tasks.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label>
            <label><span className="sr-only">筛选脚本</span><select value={scriptId} onChange={(event) => setScriptId(event.target.value)}><option value="">全部脚本</option>{choices.scripts.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label>
            <label><span className="sr-only">筛选服务器</span><select value={serverId} onChange={(event) => setServerId(event.target.value)}><option value="">全部服务器</option>{choices.servers.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label>
            <label>开始时间<input type="datetime-local" value={from} onChange={(event) => setFrom(event.target.value)} /></label>
            <label>结束时间（不含）<input type="datetime-local" value={until} onChange={(event) => setUntil(event.target.value)} /></label>
            <button type="submit" className="primary-action" disabled={loading}>查询记录</button>
          </form>
        {loading ? <p className="compact-empty" role="status">正在读取执行记录…</p> : runs.length === 0 ? <div className="large-empty"><span className="empty-server-mark" aria-hidden="true" /><h3>没有匹配的执行记录</h3><p>任务被手动执行或 Cron 触发后，运行实例会显示在这里。</p></div> : (
          <div className="table-scroll"><table className="data-table run-table"><thead><tr><th>任务与实例</th><th>状态</th><th>脚本版本</th><th>执行服务器</th><th>触发方式</th><th>进入队列</th><th><span className="sr-only">操作</span></th></tr></thead><tbody>
            {runs.map((run) => <tr key={run.id}>
              <td data-label="任务与实例"><strong>{run.taskName}</strong><span className="run-id">{shortID(run.id)}</span></td>
              <td data-label="状态"><RunStateBadge state={run.state} /></td>
              <td data-label="脚本版本"><strong>{run.scriptName}</strong><span className="cell-note">版本 {run.versionNumber}</span></td>
              <td data-label="执行服务器">{run.serverName ? <><strong>{run.serverName}</strong><span className="cell-note">{run.requiredRuntime}</span></> : <span className="unassigned-server">暂无分配服务器</span>}</td>
              <td data-label="触发方式">{triggerLabel(run.triggerType)}</td>
              <td data-label="进入队列"><time dateTime={run.queuedAt}>{formatTime(run.queuedAt)}</time></td>
              <td data-label="操作"><Link className="table-link" to={`/runs/${encodeURIComponent(run.id)}`}>查看详情</Link></td>
            </tr>)}</tbody></table></div>
        )}
        <footer className="run-history-pagination"><span role="status">第 {Math.floor((filter.offset ?? 0) / 50) + 1} 页 · {runs.length} 条记录</span><div className="row-actions"><button type="button" disabled={loading || (filter.offset ?? 0) === 0} onClick={() => setFilter((current) => ({ ...current, offset: Math.max(0, (current.offset ?? 0) - 50) }))}>上一页</button><button type="button" disabled={loading || !hasMore} onClick={() => setFilter((current) => ({ ...current, offset: (current.offset ?? 0) + 50 }))}>下一页</button></div></footer>
      </section>
    </>
  )
}

export function RunStateBadge({ state }: { state: RunState }) {
  return <span className={`run-state run-state-${state}`}><i aria-hidden="true" />{stateLabels[state]}</span>
}

function triggerLabel(trigger: RunView['triggerType']) { return trigger === 'manual' ? '手动执行' : trigger === 'schedule' ? '定时计划' : '失败重试' }
function shortID(id: string) { return id.length > 16 ? `${id.slice(0, 8)}…${id.slice(-4)}` : id }
function formatTime(value: string) { return new Intl.DateTimeFormat('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false }).format(new Date(value)) }
