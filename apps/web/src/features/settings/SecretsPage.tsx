import { FormEvent, KeyboardEvent, useEffect, useRef, useState } from 'react'

import { createSecret, getSecrets, getSession, type SecretMetadata } from '../../api/client'

export function SecretsPage() {
  const [items, setItems] = useState<SecretMetadata[]>([])
  const [isAdmin, setIsAdmin] = useState(false)
  const [loading, setLoading] = useState(true)
  const [open, setOpen] = useState(false)
  const [name, setName] = useState('')
  const [value, setValue] = useState('')
  const [error, setError] = useState('')
  const [status, setStatus] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const openButton = useRef<HTMLButtonElement>(null)
  const nameInput = useRef<HTMLInputElement>(null)
  const errorRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    let active = true
    Promise.all([getSecrets(), getSession()])
      .then(([secrets, session]) => {
        if (!active) return
        setItems(secrets)
        setIsAdmin(session.roles.includes('admin'))
      })
      .catch((reason: unknown) => { if (active) setError(reason instanceof Error ? reason.message : '读取敏感参数失败，请稍后重试') })
      .finally(() => { if (active) setLoading(false) })
    return () => { active = false }
  }, [])

  useEffect(() => { if (open) nameInput.current?.focus() }, [open])
  useEffect(() => { if (error) errorRef.current?.focus() }, [error])

  function closeDialog() {
    setOpen(false)
    setName('')
    setValue('')
    setError('')
    queueMicrotask(() => openButton.current?.focus())
  }

  async function submit(event: FormEvent) {
    event.preventDefault()
    if (!name.trim() || !value) {
      setError('请填写名称和敏感值后再保存。')
      return
    }
    setSubmitting(true)
    setError('')
    try {
      const created = await createSecret(name.trim(), value)
      setItems((current) => [created, ...current])
      setStatus(`${created.name}已加密保存；敏感值不会再次显示。`)
      closeDialog()
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '创建失败，请检查内容后重试')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <>
      <div className="page-heading">
        <div><p className="eyebrow">安全配置</p><h1>参数与密钥</h1><p>集中保存任务引用的敏感值，界面与日志永不回显明文。</p></div>
        {isAdmin ? <button ref={openButton} className="primary-action" type="button" onClick={() => { setStatus(''); setOpen(true) }}>创建敏感参数</button> : null}
      </div>

      {error && !open ? <div ref={errorRef} className="notice notice-error" role="alert" tabIndex={-1}>{error}</div> : null}
      {status ? <div className="notice notice-success" role="status">{status}</div> : null}

      <section className="security-summary" aria-label="敏感参数安全规则">
        <div><span>已保存</span><strong>{items.length}</strong><small>条敏感参数元数据</small></div>
        <div><span>加密方式</span><strong>AES-256-GCM</strong><small>每条记录使用独立数据密钥</small></div>
        <div><span>日志保护</span><strong>自动脱敏</strong><small>覆盖明文、Base64 与 URL 编码</small></div>
      </section>

      <section className="panel settings-panel" aria-labelledby="secret-list-title">
        <header className="panel-header"><div><h2 id="secret-list-title">敏感参数目录</h2><p>任务只保存引用编号，实际值仅在匹配的运行实例执行时解密。</p></div><span>{items.length} 条记录</span></header>
        {loading ? <div className="compact-empty" aria-live="polite"><span aria-hidden="true" />正在读取敏感参数…</div> : items.length === 0 ? (
          <div className="large-empty security-empty"><span className="key-mark" aria-hidden="true" /><h3>尚未创建敏感参数</h3><p>{isAdmin ? '创建后即可在任务定义中按名称引用。' : '请联系管理员创建，当前账号只能查看可引用的元数据。'}</p></div>
        ) : <div className="table-scroll"><table className="data-table settings-table"><thead><tr><th>名称</th><th>引用编号</th><th>创建人</th><th>更新时间</th></tr></thead><tbody>{items.map((item) => <tr key={item.id}><td data-label="名称"><strong>{item.name}</strong><span className="cell-note">值已隐藏</span></td><td data-label="引用编号"><code>{item.id}</code></td><td data-label="创建人">{item.createdBy || '系统'}</td><td data-label="更新时间">{formatTime(item.updatedAt)}</td></tr>)}</tbody></table></div>}
      </section>

      {open ? <div className="drawer-backdrop centered-dialog"><section className="console-dialog" role="dialog" aria-modal="true" aria-labelledby="create-secret-title" onKeyDown={(event) => trapDialog(event, closeDialog)}><header className="drawer-header"><div><p className="eyebrow">仅显示一次</p><h2 id="create-secret-title">创建敏感参数</h2><p>保存后只能按名称引用，任何页面都不会再次显示实际值。</p></div><button className="icon-button" type="button" aria-label="关闭创建敏感参数窗口" onClick={closeDialog}>×</button></header><form className="security-form" onSubmit={submit} noValidate>{error ? <div ref={errorRef} className="form-error error-summary" role="alert" tabIndex={-1}>{error}</div> : null}<div className="form-field"><label htmlFor="secret-name">名称</label><input ref={nameInput} id="secret-name" value={name} onChange={(event) => setName(event.target.value)} autoComplete="off" required aria-describedby="secret-name-help" /><small id="secret-name-help">使用易识别且不包含敏感内容的名称。</small></div><div className="form-field"><label htmlFor="secret-value">敏感值</label><textarea id="secret-value" value={value} onChange={(event) => setValue(event.target.value)} autoComplete="new-password" required aria-describedby="secret-value-help" rows={4} /><small id="secret-value-help">提交时加密，响应只包含名称和引用编号。</small></div><footer className="dialog-actions"><button className="secondary-action" type="button" onClick={closeDialog}>取消</button><button className="primary-action" type="submit" disabled={submitting}>{submitting ? '正在加密…' : '加密保存'}</button></footer></form></section></div> : null}
    </>
  )
}

function trapDialog(event: KeyboardEvent<HTMLElement>, close: () => void) {
  if (event.key === 'Escape') { event.preventDefault(); close(); return }
  if (event.key !== 'Tab') return
  const controls = Array.from(event.currentTarget.querySelectorAll<HTMLElement>('button:not(:disabled), input:not(:disabled), textarea:not(:disabled), select:not(:disabled), [tabindex="0"]'))
  if (!controls.length) return
  const first = controls[0]
  const last = controls[controls.length - 1]
  if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus() }
  else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus() }
}

function formatTime(value: string) {
  return new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value))
}
