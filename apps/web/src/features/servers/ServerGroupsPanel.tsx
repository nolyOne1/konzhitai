import { useEffect, useRef, useState, type FormEvent } from 'react'

import { createServerGroup, renameServerGroup, type ServerGroup } from '../../api/serverGroups'
import './server-groups.css'

interface Props { groups: ServerGroup[]; editable: boolean; loading: boolean; loadError: string; onReload(): void; onChanged(group: ServerGroup): void }

export function ServerGroupsPanel({ groups, editable, loading, loadError, onReload, onChanged }: Props) {
  const [name, setName] = useState('')
  const [editing, setEditing] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [status, setStatus] = useState('')
  const inputRef = useRef<HTMLInputElement>(null)
  const errorRef = useRef<HTMLDivElement>(null)
  useEffect(() => { if (error) errorRef.current?.focus() }, [error])

  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const normalized = name.trim()
    if (!normalized || [...normalized].length > 80) { setError('请输入 1 至 80 个字符的分组名称。'); return }
    setBusy(true); setError(''); setStatus('')
    try {
      const updated = editing ? await renameServerGroup(editing, normalized) : await createServerGroup(normalized)
      onChanged(updated)
      setStatus(editing ? `分组已更名为“${updated.name}”，已有发布规则保持有效。` : `已创建“${updated.name}”，可在服务器详情中分配节点。`)
      setName(''); setEditing(null); inputRef.current?.focus()
    } catch (reason) { setError(reason instanceof Error ? reason.message : '保存服务器组失败') }
    finally { setBusy(false) }
  }

  return <section className="panel server-groups-panel" aria-labelledby="server-groups-title" aria-busy={loading}>
    <header className="panel-header"><div><h2 id="server-groups-title">服务器分组</h2><p>将节点归入分组，脚本即可按组发布；移出分组请在节点详情中选择“未分组”。</p></div><span>{groups.length} 个组</span></header>
    <div className="server-groups-body">
      {loadError ? <div className="notice notice-error" role="alert">{loadError}<button type="button" className="secondary-action" onClick={onReload}>重新加载分组</button></div> : null}
      {error ? <div ref={errorRef} className="form-error" role="alert" tabIndex={-1}>{error}</div> : null}
      {status ? <p className="notice notice-success" role="status">{status}</p> : null}
      {editable ? <form className="server-group-form" onSubmit={save} noValidate>
        <label className="form-field" htmlFor="server-group-name">{editing ? '新的分组名称' : '分组名称'}<input ref={inputRef} id="server-group-name" value={name} maxLength={80} onChange={(event) => setName(event.target.value)} aria-invalid={Boolean(error)} disabled={busy} placeholder="例如：生产批处理" /></label>
        <button className="primary-action" type="submit" disabled={busy || loading}>{busy ? '正在保存…' : editing ? '保存分组名称' : '创建分组'}</button>
        {editing ? <button type="button" className="secondary-action" disabled={busy} onClick={() => { setEditing(null); setName(''); setError(''); inputRef.current?.focus() }}>取消更名</button> : null}
      </form> : null}
      {loading ? <p>正在读取分组…</p> : groups.length ? <ul className="server-group-list">{groups.map((group) => <li key={group.id}><span><strong>{group.name}</strong><small>{group.serverCount} 台服务器</small></span>{editable ? <button className="secondary-action" type="button" disabled={busy} aria-label={`更名分组${group.name}`} onClick={() => { setEditing(group.id); setName(group.name); setError(''); setStatus(''); inputRef.current?.focus() }}>更名</button> : null}</li>)}</ul> : <p className="cell-muted">尚无服务器分组。现有节点仍可使用标签调度。</p>}
    </div>
  </section>
}
