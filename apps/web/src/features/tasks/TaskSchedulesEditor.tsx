import { useEffect, useRef, useState } from 'react'

import { createTaskSchedule, deleteTaskSchedule, getTaskSchedules, updateTaskSchedule, validateTaskCron, type TaskSchedule, type TaskScheduleInput } from '../../api/client'
import { useCanExecute } from '../auth/SessionContext'

const initialSchedule: TaskScheduleInput = { cronExpression: '0 2 * * *', timezone: 'Asia/Shanghai', enabled: true }

export function TaskSchedulesEditor({ taskId }: { taskId: string }) {
  const canExecute = useCanExecute()
  const [schedules, setSchedules] = useState<TaskSchedule[]>([])
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [status, setStatus] = useState('')
  const [editing, setEditing] = useState<string | null>(null)
  const [draft, setDraft] = useState<TaskScheduleInput>(initialSchedule)
  const [removing, setRemoving] = useState<TaskSchedule | null>(null)
  const expressionRef = useRef<HTMLInputElement>(null)

  useEffect(() => {
    let active = true
    setLoading(true)
    getTaskSchedules(taskId).then((items) => { if (active) setSchedules(items) })
      .catch((reason: unknown) => { if (active) setError(message(reason)) })
      .finally(() => { if (active) setLoading(false) })
    return () => { active = false }
  }, [taskId])

  useEffect(() => { if (editing !== null) expressionRef.current?.focus() }, [editing])

  async function save() {
    if (!canExecute) return
    setBusy(true); setError(''); setStatus('')
    try {
      await validateTaskCron(draft)
      const saved = editing ? await updateTaskSchedule(taskId, editing, draft) : await createTaskSchedule(taskId, draft)
      setSchedules((items) => editing ? items.map((item) => item.id === editing ? saved : item) : [...items, saved])
      setEditing(null)
      setStatus('定时计划已保存，任务启用时按计划触发。')
    } catch (reason) { setError(message(reason)) } finally { setBusy(false) }
  }

  async function toggle(schedule: TaskSchedule) {
    if (!canExecute) return
    setBusy(true); setError(''); setStatus('')
    try {
      const saved = await updateTaskSchedule(taskId, schedule.id, { cronExpression: schedule.cronExpression, timezone: schedule.timezone, enabled: !schedule.enabled })
      setSchedules((items) => items.map((item) => item.id === saved.id ? saved : item))
      setStatus(saved.enabled ? '定时计划已启用。' : '定时计划已停用，已有执行实例不受影响。')
    } catch (reason) { setError(message(reason)) } finally { setBusy(false) }
  }

  async function remove() {
    if (!removing || !canExecute) return
    setBusy(true); setError(''); setStatus('')
    try {
      await deleteTaskSchedule(taskId, removing.id)
      setSchedules((items) => items.filter((item) => item.id !== removing.id))
      if (editing === removing.id) setEditing(null)
      setRemoving(null)
      setStatus('定时计划已删除，历史执行记录仍然保留。')
    } catch (reason) { setError(message(reason)) } finally { setBusy(false) }
  }

  return <section className="panel task-form-section" aria-labelledby="task-schedule-title" aria-busy={loading}>
    <header className="panel-header"><div><h2 id="task-schedule-title">定时计划</h2><p>计划单独保存。只有任务和计划都已启用时才会创建新的运行实例。</p></div><button type="button" className="secondary-action" disabled={!canExecute || loading || busy || editing !== null} onClick={() => { setDraft(initialSchedule); setEditing(''); setStatus('') }}>添加计划</button></header>
    {error && <div className="notice notice-error" role="alert">{error}</div>}
    {status && <div className="notice notice-success" role="status">{status}</div>}
    {loading ? <p role="status">正在读取定时计划…</p> : schedules.length === 0 ? <p className="compact-empty">尚未配置计划，可继续手动执行任务。</p> : <div className="table-scroll"><table className="data-table"><thead><tr><th>Cron 表达式</th><th>时区</th><th>计划状态</th><th>下次计划时间</th><th>操作</th></tr></thead><tbody>{schedules.map((schedule) => <tr key={schedule.id}>
      <td>{schedule.cronExpression}</td><td>{schedule.timezone}</td><td>{schedule.enabled ? '已启用' : '已停用'}</td><td>{schedule.enabled && schedule.nextRunAt ? formatScheduleTime(schedule.nextRunAt, schedule.timezone) : '—'}</td>
      <td><div className="row-actions"><button type="button" disabled={!canExecute || busy} aria-label={`编辑计划 ${schedule.cronExpression}`} onClick={() => { setDraft({ cronExpression: schedule.cronExpression, timezone: schedule.timezone, enabled: schedule.enabled }); setEditing(schedule.id); setStatus('') }}>编辑</button><button type="button" disabled={!canExecute || busy} aria-label={`${schedule.enabled ? '停用' : '启用'}计划 ${schedule.cronExpression}`} onClick={() => void toggle(schedule)}>{schedule.enabled ? '停用' : '启用'}</button><button type="button" disabled={!canExecute || busy} aria-label={`删除计划 ${schedule.cronExpression}`} onClick={() => setRemoving(schedule)}>删除</button></div></td>
    </tr>)}</tbody></table></div>}
    {editing !== null && <div className="schedule-fields"><h3>{editing ? '编辑定时计划' : '添加定时计划'}</h3><div className="task-form-grid">
      <div className="form-field"><label htmlFor="schedule-cron">计划 Cron 表达式</label><input id="schedule-cron" ref={expressionRef} aria-describedby="schedule-cron-help" value={draft.cronExpression} onChange={(event) => setDraft({ ...draft, cronExpression: event.target.value })} /><small id="schedule-cron-help">采用五段表达式，例如每天凌晨 2 点：0 2 * * *</small></div>
      <div className="form-field"><label htmlFor="schedule-timezone">计划时区</label><input id="schedule-timezone" aria-describedby="schedule-timezone-help" value={draft.timezone} onChange={(event) => setDraft({ ...draft, timezone: event.target.value })} /><small id="schedule-timezone-help">IANA 时区，例如 Asia/Shanghai 或 UTC</small></div>
      <label className="plain-checkbox"><input type="checkbox" checked={draft.enabled} onChange={(event) => setDraft({ ...draft, enabled: event.target.checked })} />计划启用</label>
    </div><div className="dialog-actions"><button type="button" className="secondary-action" disabled={!canExecute || busy} onClick={() => setEditing(null)}>取消编辑计划</button><button type="button" className="primary-action" disabled={!canExecute || busy} onClick={() => void save()}>{busy ? '正在保存计划…' : '保存计划'}</button></div></div>}
    {removing && <div className="notice"><p>确认删除计划「{removing.cronExpression}」？此操作将停止该计划后续触发。</p><div className="row-actions"><button type="button" disabled={!canExecute || busy} onClick={() => setRemoving(null)}>保留计划</button><button type="button" disabled={!canExecute || busy} onClick={() => void remove()}>确认删除计划</button></div></div>}
  </section>
}

function message(reason: unknown) { return reason instanceof Error ? reason.message : '定时计划操作失败' }

function formatScheduleTime(value: string, timezone: string) {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '时间不可用'
  try {
    return date.toLocaleString('zh-CN', { timeZone: timezone, hour12: false })
  } catch {
    return `${date.toLocaleString('zh-CN', { timeZone: 'UTC', hour12: false })} UTC（原时区：${timezone}）`
  }
}
