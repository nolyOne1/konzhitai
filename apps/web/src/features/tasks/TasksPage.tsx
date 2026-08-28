import { useEffect, useRef, useState } from 'react'
import { Link, useLocation } from 'react-router-dom'

import { getTasks, runTask, setTaskEnabled, type TaskDefinition } from '../../api/client'

export function TasksPage() {
  const location = useLocation()
  const [tasks, setTasks] = useState<TaskDefinition[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [status, setStatus] = useState((location.state as { message?: string } | null)?.message || '')
  const [busyID, setBusyID] = useState('')
  const [disableTask, setDisableTask] = useState<TaskDefinition | null>(null)
  const [cancelQueued, setCancelQueued] = useState(false)
  const confirmRef = useRef<HTMLButtonElement>(null)

  useEffect(() => {
    let active = true
    getTasks()
      .then((value) => { if (active) setTasks(value) })
      .catch((reason: unknown) => { if (active) setError(reason instanceof Error ? reason.message : '任务列表加载失败') })
      .finally(() => { if (active) setLoading(false) })
    return () => { active = false }
  }, [])

  useEffect(() => {
    if (disableTask) confirmRef.current?.focus()
  }, [disableTask])

  async function trigger(task: TaskDefinition) {
    setBusyID(task.id)
    setError('')
    setStatus('')
    try {
      await runTask(task.id)
      setStatus(`${task.name}已进入排队队列`)
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '手动执行失败')
    } finally {
      setBusyID('')
    }
  }

  function openDisable(task: TaskDefinition) {
    setCancelQueued(false)
    setDisableTask(task)
  }

  async function confirmDisable() {
    if (!disableTask) return
    setBusyID(disableTask.id)
    setError('')
    try {
      await setTaskEnabled(disableTask.id, false, cancelQueued)
      setTasks((current) => current.map((task) => task.id === disableTask.id ? { ...task, enabled: false } : task))
      setStatus(`${disableTask.name}已停用`)
      setDisableTask(null)
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '停用任务失败')
    } finally {
      setBusyID('')
    }
  }

  async function enable(task: TaskDefinition) {
    setBusyID(task.id)
    setError('')
    try {
      await setTaskEnabled(task.id, true)
      setTasks((current) => current.map((item) => item.id === task.id ? { ...item, enabled: true } : item))
      setStatus(`${task.name}已启用`)
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '启用任务失败')
    } finally {
      setBusyID('')
    }
  }

  const enabledCount = tasks.filter((task) => task.enabled).length
  const idempotentCount = tasks.filter((task) => task.idempotent).length

  return (
    <>
      <div className="page-heading">
        <div><p className="eyebrow">脚本运行规则</p><h1>任务调度</h1><p>集中管理脚本参数、执行计划和资源约束</p></div>
        <Link className="primary-action button-link" to="/tasks/new">新建任务</Link>
      </div>

      {error && <div className="notice notice-error" role="alert">{error}</div>}
      {status && <div className="notice notice-success" role="status">{status}</div>}

      <section className="task-rule-banner" aria-labelledby="queue-rule-title">
        <span className="queue-rule-mark" aria-hidden="true" />
        <div><h2 id="queue-rule-title">自动排队规则已启用</h2><p>资源不足时保持排队，服务器空闲后自动尝试分配。</p></div>
        <span>调度规则</span>
      </section>

      <section className="script-summary task-summary" aria-label="任务统计">
        <Summary label="任务总数" value={tasks.length} />
        <Summary label="已启用" value={enabledCount} />
        <Summary label="已停用" value={tasks.length - enabledCount} />
        <Summary label="允许安全重试" value={idempotentCount} />
      </section>

      <section className="panel task-list-panel" aria-labelledby="task-list-title">
        <header className="panel-header"><div><h2 id="task-list-title">任务定义</h2><p>每次执行都会锁定脚本版本并保存参数与资源快照。</p></div><span>{tasks.length} 个任务</span></header>
        {loading ? <div className="compact-empty" aria-live="polite"><span aria-hidden="true" />正在读取任务…</div> : tasks.length === 0 ? (
          <div className="large-empty"><span className="empty-script-mark" aria-hidden="true">⌁</span><h3>还没有任务</h3><p>先选择已发布脚本，再配置资源和运行计划。</p><Link className="primary-action button-link" to="/tasks/new">新建第一个任务</Link></div>
        ) : (
          <div className="table-scroll"><table className="data-table task-table"><thead><tr><th>任务</th><th>执行脚本</th><th>状态</th><th>资源与并发</th><th>排队与重试</th><th>操作</th></tr></thead><tbody>{tasks.map((task) => (
            <tr key={task.id}>
              <td data-label="任务"><Link className="script-name-link" to={`/tasks/${task.id}`}><strong>{task.name}</strong><span>{task.description || '暂无说明'}</span></Link></td>
              <td data-label="执行脚本"><strong>{task.scriptName || '未知脚本'}</strong><span className="cell-note">{task.versionPolicy === 'latest' ? '执行时锁定最新版本' : '固定指定版本'}</span></td>
              <td data-label="状态"><span className={`task-state ${task.enabled ? 'is-enabled' : 'is-disabled'}`}>{task.enabled ? '已启用' : '已停用'}</span></td>
              <td data-label="资源与并发"><strong>{task.resources.cpuMillicores}m · {formatBytes(task.resources.memoryBytes)}</strong><span className="cell-note">最大并发 {task.maxConcurrency}</span></td>
              <td data-label="排队与重试"><strong>最长等待 {formatDuration(task.maxWaitSeconds)}</strong><span className="cell-note">重试 {task.retryPolicy.maxRetries} 次 · 优先级 {task.priority}</span></td>
              <td data-label="操作"><div className="row-actions task-row-actions"><button type="button" disabled={!task.enabled || busyID === task.id} aria-label={`手动执行${task.name}`} onClick={() => void trigger(task)}>{busyID === task.id ? '处理中' : '手动执行'}</button><Link to={`/tasks/${task.id}`}>编辑</Link>{task.enabled ? <button type="button" aria-label={`停用${task.name}`} onClick={() => openDisable(task)}>停用</button> : <button type="button" disabled={busyID === task.id} aria-label={`启用${task.name}`} onClick={() => void enable(task)}>启用</button>}</div></td>
            </tr>
          ))}</tbody></table></div>
        )}
      </section>

      {disableTask && <div className="drawer-backdrop centered-dialog"><section className="console-dialog" role="dialog" aria-modal="true" aria-labelledby="disable-task-title"><header className="drawer-header"><div><p className="eyebrow">停止新的运行实例</p><h2 id="disable-task-title">停用{disableTask.name}</h2><p>停用后不会再接受手动执行，也不会由 Cron 创建新实例。</p></div><button type="button" className="icon-button" aria-label="关闭停用任务窗口" onClick={() => setDisableTask(null)}>×</button></header><div className="plain-checkbox"><input id="cancel-queued-runs" type="checkbox" aria-describedby="cancel-queued-help" checked={cancelQueued} onChange={(event) => setCancelQueued(event.target.checked)} /><span><label htmlFor="cancel-queued-runs"><strong>同时取消当前排队任务</strong></label><small id="cancel-queued-help">默认保留已排队实例，让它们在服务器空闲后继续执行。</small></span></div><footer className="dialog-actions"><button type="button" className="secondary-action" onClick={() => setDisableTask(null)}>取消</button><button ref={confirmRef} type="button" className="danger-action" disabled={busyID === disableTask.id} onClick={() => void confirmDisable()}>确认停用</button></footer></section></div>}
    </>
  )
}

function Summary({ label, value }: { label: string; value: number }) {
  return <div><span>{label}</span><strong>{value}</strong></div>
}

function formatBytes(value: number) {
  return value >= 1073741824 ? `${Math.round(value / 1073741824)} GB` : `${Math.round(value / 1048576)} MB`
}

function formatDuration(seconds: number) {
  if (seconds >= 86400) return `${Math.round(seconds / 86400)} 天`
  if (seconds >= 3600) return `${Math.round(seconds / 3600)} 小时`
  return `${seconds} 秒`
}
