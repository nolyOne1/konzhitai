import { KeyboardEvent, useEffect, useRef, useState } from 'react'

import type { Member } from '../../api/client'

export type MemberAction = 'enable' | 'disable' | 'remove' | 'restore' | 'reset'

const copy: Record<MemberAction, { title: string; confirm: string; description: string }> = {
  enable: { title: '确认启用成员', confirm: '确认启用', description: '启用后，该成员可以使用自己的密码登录系统。' },
  disable: { title: '确认停用成员', confirm: '确认停用', description: '停用后，该成员的所有现有会话会立即失效，直到管理员重新启用账号。' },
  remove: { title: '确认移除成员', confirm: '确认移除', description: '移除后，该成员的所有现有会话会立即失效；历史任务、日志和审计记录会继续保留。' },
  restore: { title: '确认恢复成员', confirm: '确认恢复', description: '恢复后，账号会保持停用状态；请在确认后手动启用。' },
  reset: { title: '确认重置密码', confirm: '确认重置密码', description: '重置后，该成员的所有现有会话会立即失效，并必须使用新的临时密码完成首次改密。' },
}

export function MemberActionDialog({ member, action, onConfirm, onClose }: { member: Member; action: MemberAction; onConfirm(): Promise<void>; onClose(): void }) {
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const errorRef = useRef<HTMLDivElement>(null)
  const confirmButton = useRef<HTMLButtonElement>(null)
  const details = copy[action]
  useEffect(() => { confirmButton.current?.focus() }, [])
  useEffect(() => { if (error) errorRef.current?.focus() }, [error])

  async function confirm() {
    setSaving(true)
    try {
      setError('')
      await onConfirm()
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '成员操作失败，请重试')
    } finally { setSaving(false) }
  }

  return (
    <div className="drawer-backdrop centered-dialog">
      <section className="console-dialog action-dialog" role="dialog" aria-modal="true" aria-labelledby="member-action-title" onKeyDown={(event) => trapDialog(event, onClose)}>
        <header className="drawer-header"><div><p className="eyebrow">成员生命周期</p><h2 id="member-action-title">{details.title}</h2><p>{member.displayName} · {member.email}</p></div><button className="icon-button" type="button" aria-label={`关闭${details.title}窗口`} onClick={onClose}>×</button></header>
        {error ? <div ref={errorRef} className="form-error error-summary" role="alert" tabIndex={-1}>{error}</div> : null}
        <p className={action === 'remove' || action === 'disable' || action === 'reset' ? 'action-impact action-impact-danger' : 'action-impact'}>{details.description}</p>
        <footer className="dialog-actions"><button className="secondary-action" type="button" onClick={onClose}>取消</button><button ref={confirmButton} className={action === 'remove' ? 'danger-action' : 'primary-action'} type="button" disabled={saving} onClick={() => void confirm()}>{saving ? '正在处理…' : details.confirm}</button></footer>
      </section>
    </div>
  )
}

function trapDialog(event: KeyboardEvent<HTMLElement>, close: () => void) {
  if (event.key === 'Escape') { event.preventDefault(); close(); return }
  if (event.key !== 'Tab') return
  const controls = Array.from(event.currentTarget.querySelectorAll<HTMLElement>('button:not(:disabled), input:not(:disabled), [tabindex="0"]'))
  const first = controls[0]
  const last = controls.at(-1)
  if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last?.focus() }
  else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first?.focus() }
}
