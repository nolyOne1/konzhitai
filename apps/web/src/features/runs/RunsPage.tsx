import { useEffect, useMemo, useState } from 'react'
import { Link } from 'react-router-dom'

import { getRuns, type RunState, type RunView } from '../../api/client'

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

  useEffect(() => {
    let active = true
    getRuns()
      .then((items) => { if (active) setRuns(items) })
      .catch((reason: unknown) => { if (active) setError(reason instanceof Error ? reason.message : '读取执行记录失败') })
      .finally(() => { if (active) setLoading(false) })
    return () => { active = false }
  }, [])

  const visible = useMemo(() => runs.filter((run) => {
    const matchesState = state === 'all' || run.state === state
    const keyword = query.trim().toLocaleLowerCase('zh-CN')
    return matchesState && (!keyword || `${run.taskName} ${run.scriptName} ${run.serverName ?? ''} ${run.id}`.toLocaleLowerCase('zh-CN').includes(keyword))
  }), [query, runs, state])

  return (
    <>
      <div className="page-heading">
        <div><p className="eyebrow">运行历史与当前状态</p><h1>执行记录</h1><p>排队是任务的运行状态；没有合适服务器时继续等待，资源恢复后自动调度。</p></div>
      </div>
      {error && <div className="notice notice-error" role="alert">{error}</div>}
      <section className="run-summary" aria-label="执行概况">
        <div><span>当前运行</span><strong>{loading ? '—' : runs.filter((run) => run.state === 'running').length}</strong></div>
        <div><span>排队等待</span><strong>{loading ? '—' : runs.filter((run) => run.state === 'queued').length}</strong></div>
        <div><span>待确认</span><strong>{loading ? '—' : runs.filter((run) => run.state === 'unknown').length}</strong></div>
        <div><span>本页记录</span><strong>{loading ? '—' : runs.length}</strong></div>
      </section>
      <section className="panel run-list-panel" aria-labelledby="run-list-title" aria-busy={loading}>
        <header className="run-list-toolbar">
          <div><h2 id="run-list-title">运行实例</h2><p>按任务、脚本、服务器或实例编号查找。</p></div>
          <div className="run-filters">
            <label><span className="sr-only">筛选执行记录</span><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索执行记录" /></label>
            <label><span className="sr-only">筛选运行状态</span><select value={state} onChange={(event) => setState(event.target.value as RunState | 'all')}><option value="all">全部状态</option>{Object.entries(stateLabels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></label>
          </div>
        </header>
        {!loading && visible.length === 0 ? <div className="large-empty"><span className="empty-server-mark" aria-hidden="true" /><h3>没有匹配的执行记录</h3><p>任务被手动执行或 Cron 触发后，运行实例会显示在这里。</p></div> : (
          <div className="table-scroll"><table className="data-table run-table"><thead><tr><th>任务与实例</th><th>状态</th><th>脚本版本</th><th>执行服务器</th><th>触发方式</th><th>进入队列</th><th><span className="sr-only">操作</span></th></tr></thead><tbody>
            {visible.map((run) => <tr key={run.id}>
              <td data-label="任务与实例"><strong>{run.taskName}</strong><span className="run-id">{shortID(run.id)}</span></td>
              <td data-label="状态"><RunStateBadge state={run.state} /></td>
              <td data-label="脚本版本"><strong>{run.scriptName}</strong><span className="cell-note">版本 {run.versionNumber}</span></td>
              <td data-label="执行服务器">{run.serverName ? <><strong>{run.serverName}</strong><span className="cell-note">{run.requiredRuntime}</span></> : <span className="unassigned-server">暂无分配服务器</span>}</td>
              <td data-label="触发方式">{triggerLabel(run.triggerType)}</td>
              <td data-label="进入队列"><time dateTime={run.queuedAt}>{formatTime(run.queuedAt)}</time></td>
              <td data-label="操作"><Link className="table-link" to={`/runs/${encodeURIComponent(run.id)}`}>查看详情</Link></td>
            </tr>)}</tbody></table></div>
        )}
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
