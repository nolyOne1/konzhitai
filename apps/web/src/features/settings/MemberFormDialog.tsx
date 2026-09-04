import { FormEvent, KeyboardEvent, useEffect, useRef, useState } from 'react'

import type { CreateMemberInput, RoleName } from '../../api/client'

export const memberRoleOptions: { value: RoleName; label: string; description: string }[] = [
  { value: 'admin', label: '管理员', description: '系统配置、成员权限与全部操作' },
  { value: 'operator', label: '运维人员', description: '执行、终止、重试任务与处理告警' },
  { value: 'developer', label: '脚本开发者', description: '编辑、发布脚本并引用敏感参数' },
  { value: 'viewer', label: '只读成员', description: '查看运行状态、日志和配置元数据' },
]

export function MemberFormDialog({ onSubmit, onClose }: { onSubmit(input: CreateMemberInput): Promise<void>; onClose(): void }) {
  const [displayName, setDisplayName] = useState('')
  const [email, setEmail] = useState('')
  const [roles, setRoles] = useState<RoleName[]>([])
  const [error, setError] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const nameInput = useRef<HTMLInputElement>(null)
  const errorRef = useRef<HTMLDivElement>(null)

  useEffect(() => { nameInput.current?.focus() }, [])
  useEffect(() => { if (error) errorRef.current?.focus() }, [error])

  function toggleRole(role: RoleName) {
    setRoles((current) => current.includes(role) ? current.filter((item) => item !== role) : [...current, role])
  }

  async function submit(event: FormEvent) {
    event.preventDefault()
    if (!displayName.trim() || !email.trim() || roles.length === 0) {
      setError('请填写姓名、邮箱并至少选择一个角色。')
      return
    }
    setSubmitting(true)
    setError('')
    try {
      await onSubmit({ displayName: displayName.trim(), email: email.trim(), roles })
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '创建成员失败，请检查后重试')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div className="drawer-backdrop centered-dialog">
      <section className="console-dialog" role="dialog" aria-modal="true" aria-labelledby="create-member-title" onKeyDown={(event) => trapDialog(event, onClose)}>
        <header className="drawer-header"><div><p className="eyebrow">直接创建账号</p><h2 id="create-member-title">创建成员</h2><p>系统会生成一次性临时密码，请在创建后安全地交给成员。</p></div><button className="icon-button" type="button" aria-label="关闭创建成员窗口" onClick={onClose}>×</button></header>
        <form className="security-form" onSubmit={submit} noValidate>
          {error ? <div ref={errorRef} className="form-error error-summary" role="alert" tabIndex={-1}>{error}</div> : null}
          <div className="form-field"><label htmlFor="member-display-name">姓名</label><input ref={nameInput} id="member-display-name" value={displayName} onChange={(event) => setDisplayName(event.target.value)} autoComplete="name" required /></div>
          <div className="form-field"><label htmlFor="member-email">邮箱</label><input id="member-email" type="email" value={email} onChange={(event) => setEmail(event.target.value)} autoComplete="email" required /></div>
          <fieldset className="role-selector"><legend>角色</legend>{memberRoleOptions.map((role) => <label key={role.value} className="role-choice"><input type="checkbox" aria-label={role.label} checked={roles.includes(role.value)} onChange={() => toggleRole(role.value)} /><span><strong>{role.label}</strong><small>{role.description}</small></span></label>)}</fieldset>
          <footer className="dialog-actions"><button className="secondary-action" type="button" onClick={onClose}>取消</button><button className="primary-action" type="submit" disabled={submitting}>{submitting ? '正在创建…' : '确认创建'}</button></footer>
        </form>
      </section>
    </div>
  )
}

function trapDialog(event: KeyboardEvent<HTMLElement>, close: () => void) {
  if (event.key === 'Escape') { event.preventDefault(); close(); return }
  if (event.key !== 'Tab') return
  const controls = Array.from(event.currentTarget.querySelectorAll<HTMLElement>('button:not(:disabled), input:not(:disabled), textarea:not(:disabled), select:not(:disabled), [tabindex="0"]'))
  const first = controls[0]
  const last = controls.at(-1)
  if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last?.focus() }
  else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first?.focus() }
}
