import { KeyboardEvent, type RefObject, useEffect, useRef, useState } from 'react'

import {
  createMember, getMembers, getSession, removeMember, resetMemberPassword, restoreMember, setMemberEnabled, updateMemberRoles,
  type CreateMemberInput, type Member, type MemberStatus, type RoleName,
} from '../../api/client'
import { MemberActionDialog, type MemberAction } from './MemberActionDialog'
import { MemberFormDialog, memberRoleOptions } from './MemberFormDialog'
import { TemporaryPasswordDialog } from './TemporaryPasswordDialog'

type Dialog =
  | { kind: 'create' }
  | { kind: 'roles'; member: Member }
  | { kind: 'action'; member: Member; action: MemberAction }
  | { kind: 'password'; password: string }

const filters: Array<{ value: MemberStatus; label: string }> = [
  { value: 'all', label: '全部' }, { value: 'active', label: '已启用' }, { value: 'disabled', label: '已停用' }, { value: 'removed', label: '已移除' },
]

export function MembersPage() {
  const [members, setMembers] = useState<Member[]>([])
  const [isAdmin, setIsAdmin] = useState(false)
  const [sessionID, setSessionID] = useState('')
  const [filter, setFilter] = useState<MemberStatus>('all')
  const [loading, setLoading] = useState(true)
  const [dialog, setDialog] = useState<Dialog | null>(null)
  const [openedMenu, setOpenedMenu] = useState<string | null>(null)
  const [selected, setSelected] = useState<RoleName[]>([])
  const [error, setError] = useState('')
  const [status, setStatus] = useState('')
  const [savingRoles, setSavingRoles] = useState(false)
  const firstRole = useRef<HTMLInputElement>(null)
  const trigger = useRef<HTMLElement | null>(null)
  const errorRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    let active = true
    setLoading(true)
    Promise.all([getMembers(filter), getSession()]).then(([items, session]) => {
      if (!active) return
      setMembers(items)
      setIsAdmin(session.roles.includes('admin'))
      setSessionID(session.id)
    }).catch((reason: unknown) => { if (active) setError(reason instanceof Error ? reason.message : '读取团队成员失败') })
      .finally(() => { if (active) setLoading(false) })
    return () => { active = false }
  }, [filter])

  useEffect(() => { if (dialog?.kind === 'roles') firstRole.current?.focus() }, [dialog])
  useEffect(() => { if (error) errorRef.current?.focus() }, [error])

  function openDialog(next: Dialog, element: HTMLElement) {
    trigger.current = element
    setOpenedMenu(null)
    setError('')
    setStatus('')
    if (next.kind === 'roles') setSelected(next.member.roles)
    setDialog(next)
  }

  function closeDialog() {
    setDialog(null)
    setError('')
    queueMicrotask(() => trigger.current?.focus())
  }

  function changeFilter(next: MemberStatus) {
    setError('')
    setStatus('')
    setOpenedMenu(null)
    setFilter(next)
  }

  async function submitCreate(input: CreateMemberInput) {
    const result = await createMember(input)
    setMembers((current) => filter === 'removed' ? current : [result.member, ...current])
    setStatus(`${result.member.displayName}已创建，请安全交付临时密码。`)
    setDialog({ kind: 'password', password: result.temporaryPassword })
  }

  async function saveRoles() {
    if (dialog?.kind !== 'roles' || selected.length === 0) { setError('请至少保留一个角色。'); return }
    setSavingRoles(true)
    setError('')
    try {
      const updated = await updateMemberRoles(dialog.member.id, selected)
      setMembers((current) => current.map((item) => item.id === updated.id ? updated : item))
      setStatus(`${updated.displayName}的角色已更新。`)
      closeDialog()
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '保存角色失败，请重试')
    } finally { setSavingRoles(false) }
  }

  function toggleRole(role: RoleName) {
    setSelected((current) => current.includes(role) ? current.filter((item) => item !== role) : [...current, role])
  }

  async function runAction(member: Member, action: MemberAction) {
    try {
      if (action === 'enable' || action === 'disable') {
        const updated = await setMemberEnabled(member.id, action === 'enable')
        setMembers((current) => current.map((item) => item.id === updated.id ? updated : item))
        setStatus(`${updated.displayName}已${action === 'enable' ? '启用' : '停用'}。`)
      } else if (action === 'remove') {
        await removeMember(member.id)
        setMembers((current) => current.filter((item) => item.id !== member.id))
        setStatus(`${member.displayName}已移除。`)
      } else if (action === 'restore') {
        const updated = await restoreMember(member.id)
        setMembers((current) => current.map((item) => item.id === updated.id ? updated : item))
        setStatus(`${updated.displayName}已恢复，账号保持停用状态。`)
      } else {
        const result = await resetMemberPassword(member.id)
        setMembers((current) => current.map((item) => item.id === result.member.id ? result.member : item))
        setStatus(`${member.displayName}的密码已重置，请安全交付临时密码。`)
        setDialog({ kind: 'password', password: result.temporaryPassword })
        return
      }
      closeDialog()
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '成员操作失败，请重试')
      throw reason
    }
  }

  return (
    <>
      <div className="page-heading"><div><p className="eyebrow">团队协作</p><h1>团队与权限</h1><p>按职责组合角色，让成员只拥有完成工作所需的权限。</p></div>{isAdmin ? <button className="primary-action" type="button" onClick={(event) => openDialog({ kind: 'create' }, event.currentTarget)}>创建成员</button> : null}</div>
      {error && !dialog ? <div ref={errorRef} className="notice notice-error" role="alert" tabIndex={-1}>{error}</div> : null}
      {status ? <div className="notice notice-success" role="status">{status}</div> : null}
      <section className="role-grid" aria-label="角色权限说明">{memberRoleOptions.map((role) => <article key={role.value}><span className={`role-mark role-${role.value}`} aria-hidden="true" /><div><h2>{role.label}</h2><p>{role.description}</p></div></article>)}</section>
      <section className="panel settings-panel" aria-labelledby="member-list-title">
        <header className="panel-header member-panel-header"><div><h2 id="member-list-title">团队成员</h2><p>一个成员可拥有多个角色，权限取并集。</p></div><span>{members.length} 位成员</span></header>
        <div className="member-filters" aria-label="成员状态筛选">{filters.map((item) => <button key={item.value} className="filter-button" type="button" aria-pressed={filter === item.value} onClick={() => changeFilter(item.value)}>{item.label}</button>)}</div>
        {loading ? <div className="compact-empty" aria-live="polite"><span aria-hidden="true" />正在读取成员…</div> : members.length === 0 ? <div className="large-empty"><span className="member-mark" aria-hidden="true" /><h3>尚无{filter === 'removed' ? '已移除' : ''}成员</h3><p>{filter === 'removed' ? '已移除成员会保留历史记录，并可在此恢复。' : '先完成首个管理员账号初始化，再创建团队成员。'}</p></div> : <div className="table-scroll"><table className="data-table settings-table"><thead><tr><th>成员</th><th>账号状态</th><th>角色</th><th>加入时间</th>{isAdmin ? <th>操作</th> : null}</tr></thead><tbody>{members.map((member) => <tr key={member.id}><td data-label="成员"><strong>{member.displayName}</strong><span className="cell-note">{member.email}</span></td><td data-label="账号状态"><span className={`status-badge ${member.removedAt ? 'status-badge-disabled' : member.enabled ? 'status-badge-online' : 'status-badge-disabled'}`}><i aria-hidden="true" />{member.removedAt ? '已移除' : member.enabled ? '已启用' : '已停用'}</span></td><td data-label="角色"><div className="tag-list">{member.roles.map((role) => <span key={role}>{roleLabel(role)}</span>)}</div></td><td data-label="加入时间">{formatDate(member.createdAt)}</td>{isAdmin ? <td data-label="操作">{member.id !== sessionID ? <div className="member-actions"><button className="table-action" type="button" aria-label={`更多${member.displayName}操作`} aria-haspopup="menu" aria-expanded={openedMenu === member.id} onClick={() => setOpenedMenu((current) => current === member.id ? null : member.id)}>更多操作</button>{openedMenu === member.id ? <div className="member-action-menu" role="menu" aria-label={`${member.displayName}的成员操作`}><button role="menuitem" type="button" onClick={(event) => openDialog({ kind: 'roles', member }, menuTrigger(event.currentTarget))}>调整角色</button>{member.removedAt ? <button role="menuitem" type="button" onClick={(event) => openDialog({ kind: 'action', member, action: 'restore' }, menuTrigger(event.currentTarget))}>恢复成员</button> : <><button role="menuitem" type="button" onClick={(event) => openDialog({ kind: 'action', member, action: member.enabled ? 'disable' : 'enable' }, menuTrigger(event.currentTarget))}>{member.enabled ? '停用成员' : '启用成员'}</button><button role="menuitem" type="button" onClick={(event) => openDialog({ kind: 'action', member, action: 'reset' }, menuTrigger(event.currentTarget))}>重置密码</button><button role="menuitem" type="button" onClick={(event) => openDialog({ kind: 'action', member, action: 'remove' }, menuTrigger(event.currentTarget))}>移除成员</button></>}</div> : null}</div> : <span className="cell-note">当前账号</span>}</td> : null}</tr>)}</tbody></table></div>}
      </section>
      {dialog?.kind === 'create' ? <MemberFormDialog onSubmit={submitCreate} onClose={closeDialog} /> : null}
      {dialog?.kind === 'roles' ? <RoleDialog member={dialog.member} selected={selected} saving={savingRoles} error={error} firstRole={firstRole} onToggle={toggleRole} onSave={() => void saveRoles()} onClose={closeDialog} /> : null}
      {dialog?.kind === 'action' ? <MemberActionDialog member={dialog.member} action={dialog.action} onConfirm={() => runAction(dialog.member, dialog.action)} onClose={closeDialog} /> : null}
      {dialog?.kind === 'password' ? <TemporaryPasswordDialog password={dialog.password} onClose={closeDialog} returnFocusTo={trigger.current} /> : null}
    </>
  )
}

function RoleDialog({ member, selected, saving, error, firstRole, onToggle, onSave, onClose }: { member: Member; selected: RoleName[]; saving: boolean; error: string; firstRole: RefObject<HTMLInputElement | null>; onToggle(role: RoleName): void; onSave(): void; onClose(): void }) {
  return <div className="drawer-backdrop centered-dialog"><section className="console-dialog" role="dialog" aria-modal="true" aria-labelledby="edit-role-title" onKeyDown={(event) => trapDialog(event, onClose)}><header className="drawer-header"><div><p className="eyebrow">最小权限原则</p><h2 id="edit-role-title">调整成员角色</h2><p>{member.displayName} · {member.email}</p></div><button className="icon-button" type="button" aria-label="关闭调整成员角色窗口" onClick={onClose}>×</button></header><div className="role-selector">{error ? <div className="form-error error-summary" role="alert" tabIndex={-1}>{error}</div> : null}{memberRoleOptions.map((role, index) => <label key={role.value} className="role-choice"><input ref={index === 0 ? firstRole : undefined} aria-label={role.label} type="checkbox" checked={selected.includes(role.value)} onChange={() => onToggle(role.value)} /><span><strong>{role.label}</strong><small>{role.description}</small></span></label>)}</div><footer className="dialog-actions"><button className="secondary-action" type="button" onClick={onClose}>取消</button><button className="primary-action" type="button" disabled={saving} onClick={onSave}>{saving ? '正在保存…' : '保存角色'}</button></footer></section></div>
}

function roleLabel(role: RoleName) { return memberRoleOptions.find((item) => item.value === role)?.label || role }
function formatDate(value: string) { return new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium' }).format(new Date(value)) }
function menuTrigger(element: HTMLElement) { return element.closest('.member-actions')?.querySelector<HTMLElement>('.table-action') ?? element }
function trapDialog(event: KeyboardEvent<HTMLElement>, close: () => void) {
  if (event.key === 'Escape') { event.preventDefault(); close(); return }
  if (event.key !== 'Tab') return
  const controls = Array.from(event.currentTarget.querySelectorAll<HTMLElement>('button:not(:disabled), input:not(:disabled), [tabindex="0"]'))
  const first = controls[0]
  const last = controls.at(-1)
  if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last?.focus() }
  else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first?.focus() }
}
