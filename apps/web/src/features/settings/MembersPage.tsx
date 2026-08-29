import { KeyboardEvent, useEffect, useRef, useState } from 'react'

import { getMembers, getSession, updateMemberRoles, type Member, type RoleName } from '../../api/client'

const roleOptions: { value: RoleName; label: string; description: string }[] = [
  { value: 'admin', label: '管理员', description: '系统配置、成员权限与全部操作' },
  { value: 'operator', label: '运维人员', description: '执行、终止、重试任务与处理告警' },
  { value: 'developer', label: '脚本开发者', description: '编辑、发布脚本并引用敏感参数' },
  { value: 'viewer', label: '只读成员', description: '查看运行状态、日志和配置元数据' },
]

export function MembersPage() {
  const [members, setMembers] = useState<Member[]>([])
  const [isAdmin, setIsAdmin] = useState(false)
  const [loading, setLoading] = useState(true)
  const [editing, setEditing] = useState<Member | null>(null)
  const [selected, setSelected] = useState<RoleName[]>([])
  const [error, setError] = useState('')
  const [status, setStatus] = useState('')
  const [saving, setSaving] = useState(false)
  const firstRole = useRef<HTMLInputElement>(null)
  const trigger = useRef<HTMLButtonElement | null>(null)
  const errorRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    let active = true
    Promise.all([getMembers(), getSession()]).then(([items, session]) => {
      if (!active) return
      setMembers(items); setIsAdmin(session.roles.includes('admin'))
    }).catch((reason: unknown) => { if (active) setError(reason instanceof Error ? reason.message : '读取团队成员失败') })
      .finally(() => { if (active) setLoading(false) })
    return () => { active = false }
  }, [])

  useEffect(() => { if (editing) firstRole.current?.focus() }, [editing])
  useEffect(() => { if (error) errorRef.current?.focus() }, [error])

  function openEditor(member: Member, button: HTMLButtonElement) {
    trigger.current = button
    setEditing(member); setSelected(member.roles); setError(''); setStatus('')
  }

  function closeEditor() {
    setEditing(null); setError('')
    queueMicrotask(() => trigger.current?.focus())
  }

  function toggle(role: RoleName) {
    setSelected((current) => current.includes(role) ? current.filter((item) => item !== role) : [...current, role])
  }

  async function save() {
    if (!editing || selected.length === 0) { setError('请至少保留一个角色。'); return }
    setSaving(true); setError('')
    try {
      const updated = await updateMemberRoles(editing.id, selected)
      setMembers((current) => current.map((item) => item.id === updated.id ? updated : item))
      setStatus(`${updated.displayName}的角色已更新。`)
      closeEditor()
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '保存角色失败，请重试')
    } finally { setSaving(false) }
  }

  return (
    <>
      <div className="page-heading"><div><p className="eyebrow">团队协作</p><h1>团队与权限</h1><p>按职责组合角色，让成员只拥有完成工作所需的权限。</p></div></div>
      {error && !editing ? <div ref={errorRef} className="notice notice-error" role="alert" tabIndex={-1}>{error}</div> : null}
      {status ? <div className="notice notice-success" role="status">{status}</div> : null}
      <section className="role-grid" aria-label="角色权限说明">{roleOptions.map((role) => <article key={role.value}><span className={`role-mark role-${role.value}`} aria-hidden="true" /><div><h2>{role.label}</h2><p>{role.description}</p></div></article>)}</section>
      <section className="panel settings-panel" aria-labelledby="member-list-title"><header className="panel-header"><div><h2 id="member-list-title">团队成员</h2><p>一个成员可拥有多个角色，权限取并集。</p></div><span>{members.length} 位成员</span></header>{loading ? <div className="compact-empty" aria-live="polite"><span aria-hidden="true" />正在读取成员…</div> : members.length === 0 ? <div className="large-empty"><span className="member-mark" aria-hidden="true" /><h3>尚无团队成员</h3><p>先完成首个管理员账号初始化，再邀请团队加入。</p></div> : <div className="table-scroll"><table className="data-table settings-table"><thead><tr><th>成员</th><th>账号状态</th><th>角色</th><th>加入时间</th>{isAdmin ? <th>操作</th> : null}</tr></thead><tbody>{members.map((member) => <tr key={member.id}><td data-label="成员"><strong>{member.displayName}</strong><span className="cell-note">{member.email}</span></td><td data-label="账号状态"><span className={`status-badge ${member.enabled ? 'status-badge-online' : 'status-badge-disabled'}`}><i aria-hidden="true" />{member.enabled ? '已启用' : '已停用'}</span></td><td data-label="角色"><div className="tag-list">{member.roles.map((role) => <span key={role}>{roleLabel(role)}</span>)}</div></td><td data-label="加入时间">{formatDate(member.createdAt)}</td>{isAdmin ? <td data-label="操作"><button className="table-action" type="button" aria-label={`调整${member.displayName}的角色`} onClick={(event) => openEditor(member, event.currentTarget)}>调整角色</button></td> : null}</tr>)}</tbody></table></div>}</section>
      {editing ? <div className="drawer-backdrop centered-dialog"><section className="console-dialog" role="dialog" aria-modal="true" aria-labelledby="edit-role-title" onKeyDown={(event) => trapDialog(event, closeEditor)}><header className="drawer-header"><div><p className="eyebrow">最小权限原则</p><h2 id="edit-role-title">调整成员角色</h2><p>{editing.displayName} · {editing.email}</p></div><button className="icon-button" type="button" aria-label="关闭调整成员角色窗口" onClick={closeEditor}>×</button></header><div className="role-selector">{error ? <div ref={errorRef} className="form-error error-summary" role="alert" tabIndex={-1}>{error}</div> : null}{roleOptions.map((role, index) => <label key={role.value} className="role-choice"><input ref={index === 0 ? firstRole : undefined} aria-label={role.label} type="checkbox" checked={selected.includes(role.value)} onChange={() => toggle(role.value)} /><span><strong>{role.label}</strong><small>{role.description}</small></span></label>)}</div><footer className="dialog-actions"><button className="secondary-action" type="button" onClick={closeEditor}>取消</button><button className="primary-action" type="button" disabled={saving} onClick={() => void save()}>{saving ? '正在保存…' : '保存角色'}</button></footer></section></div> : null}
    </>
  )
}

function roleLabel(role: RoleName) { return roleOptions.find((item) => item.value === role)?.label || role }
function formatDate(value: string) { return new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium' }).format(new Date(value)) }
function trapDialog(event: KeyboardEvent<HTMLElement>, close: () => void) {
  if (event.key === 'Escape') { event.preventDefault(); close(); return }
  if (event.key !== 'Tab') return
  const controls = Array.from(event.currentTarget.querySelectorAll<HTMLElement>('button:not(:disabled), input:not(:disabled), [tabindex="0"]'))
  if (!controls.length) return
  if (event.shiftKey && document.activeElement === controls[0]) { event.preventDefault(); controls.at(-1)?.focus() }
  else if (!event.shiftKey && document.activeElement === controls.at(-1)) { event.preventDefault(); controls[0].focus() }
}
