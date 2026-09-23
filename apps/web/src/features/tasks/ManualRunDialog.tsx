import { useId, useState } from 'react'

import { runTask, type TaskDefinition, type TaskRun } from '../../api/client'
import { useCanExecute } from '../auth/SessionContext'

export function ManualRunDialog({ task, rerun = false, onClose, onStarted }: { task: TaskDefinition; rerun?: boolean; onClose: () => void; onStarted: (run: TaskRun) => void }) {
  const canExecute = useCanExecute()
  const id = useId()
  const [parameters, setParameters] = useState(JSON.stringify(task.parameters, null, 2))
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  async function submit(event: React.FormEvent) {
    event.preventDefault()
    if (busy || !canExecute || !task.enabled) return
    let values: Record<string, unknown>
    try {
      const parsed: unknown = JSON.parse(parameters)
      if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) throw new Error('invalid')
      values = parsed as Record<string, unknown>
    } catch { setError('本次参数必须是 JSON 对象。'); return }
    setBusy(true); setError('')
    try { onStarted(await runTask(task.id, values)) }
    catch (reason) { setError(reason instanceof Error ? reason.message : '创建执行实例失败') }
    finally { setBusy(false) }
  }

  return <div className="drawer-backdrop centered-dialog"><section className="console-dialog" role="dialog" aria-modal="true" aria-labelledby={`${id}-title`}>
    <header className="drawer-header"><div><p className="eyebrow">创建新的运行实例</p><h2 id={`${id}-title`}>{rerun ? '按当前任务配置再次执行' : '手动执行'}{task.name}</h2><p>{rerun ? '本次使用当前任务的参数、资源和版本策略，可能与历史实例不同。' : '可修改本次普通参数，不会改写任务默认配置。'}</p></div><button type="button" className="icon-button" disabled={busy} aria-label="关闭手动执行窗口" onClick={onClose}>×</button></header>
    {!task.enabled && <div className="notice" role="status">任务已停用，请先启用任务。</div>}
    {!canExecute && <div className="notice" role="status">当前角色只能查看任务。</div>}
    {error && <div className="notice notice-error" role="alert">{error}</div>}
    <p>{task.versionPolicy === 'latest' ? '版本策略：执行时锁定最新已发布版本' : `版本策略：固定版本 ${task.pinnedVersionId}`}</p>
    <form onSubmit={(event) => void submit(event)}><div className="form-field"><label htmlFor={`${id}-parameters`}>本次普通参数（JSON）</label><textarea id={`${id}-parameters`} value={parameters} disabled={busy || !canExecute} onChange={(event) => setParameters(event.target.value)} aria-describedby={`${id}-help`} /><small id={`${id}-help`}>字段会覆盖当前默认值，未填写的字段仍使用默认值。敏感参数沿用任务密钥引用，请勿填写明文。</small></div><footer className="dialog-actions"><button type="button" className="secondary-action" disabled={busy} onClick={onClose}>返回</button><button type="submit" className="primary-action" disabled={busy || !task.enabled || !canExecute}>{busy ? '正在创建…' : '确认执行'}</button></footer></form>
  </section></div>
}
