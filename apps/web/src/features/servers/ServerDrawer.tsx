import { useEffect, useState, type FormEvent } from 'react'

import type { ServerView, UpdateServerInput } from '../../api/client'

interface ServerDrawerProps {
  server: ServerView
  saving: boolean
  securityBusy: boolean
  isAdmin: boolean
  onClose: () => void
  onSave: (input: UpdateServerInput) => Promise<void>
  onRotate: () => Promise<string>
  onRevoke: () => Promise<void>
}

export function ServerDrawer({ server, saving, securityBusy, isAdmin, onClose, onSave, onRotate, onRevoke }: ServerDrawerProps) {
  const [name, setName] = useState(server.name)
  const [weight, setWeight] = useState(String(server.schedulingWeight))
  const [labels, setLabels] = useState(formatLabels(server.labels))
  const [credential, setCredential] = useState('')
  const [securityStatus, setSecurityStatus] = useState('')
  const [securityError, setSecurityError] = useState('')
  const [confirmRevoke, setConfirmRevoke] = useState(false)

  useEffect(() => {
    setName(server.name)
    setWeight(String(server.schedulingWeight))
    setLabels(formatLabels(server.labels))
  }, [server])

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    await onSave({
      name: name.trim(),
      schedulingWeight: Number(weight),
      labels: parseLabels(labels),
    })
  }

  async function rotate() {
    setSecurityError(''); setSecurityStatus(''); setCredential('')
    try {
      setCredential(await onRotate())
      setSecurityStatus('新凭据已签发；代理首次使用它连接后，旧凭据会自动失效。')
    } catch (reason) { setSecurityError(reason instanceof Error ? reason.message : '轮换代理凭据失败') }
  }

  async function revoke() {
    setSecurityError(''); setSecurityStatus(''); setCredential('')
    try {
      await onRevoke()
      setConfirmRevoke(false)
      setSecurityStatus('代理凭据已全部吊销，节点连接已断开。')
    } catch (reason) { setSecurityError(reason instanceof Error ? reason.message : '紧急吊销失败') }
  }

  return (
    <div className="drawer-backdrop" onMouseDown={(event) => {
      if (event.target === event.currentTarget) onClose()
    }}>
      <aside className="server-drawer" role="dialog" aria-modal="true" aria-label="服务器详情">
        <header className="drawer-header">
          <div>
            <p className="eyebrow">执行节点</p>
            <h2>{server.name}</h2>
            <p>{server.cloudProvider || '未分类'} · {server.region || '未设置地域'}</p>
          </div>
          <button type="button" className="icon-button" aria-label="关闭服务器详情" onClick={onClose}>
            <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M6 6l12 12M18 6L6 18" /></svg>
          </button>
        </header>

        <dl className="server-facts">
          <div><dt>当前状态</dt><dd>{statusLabel(server)}</dd></div>
          <div><dt>运行环境</dt><dd>{server.runtimes.length ? server.runtimes.join('、') : '未上报'}</dd></div>
          <div><dt>代理版本</dt><dd>{server.agentVersion || '未上报'}</dd></div>
          <div><dt>运行任务</dt><dd>{server.runningTasks} 个</dd></div>
        </dl>

        <form className="drawer-form" onSubmit={handleSubmit}>
          <label className="form-field">
            服务器名称
            <input value={name} onChange={(event) => setName(event.target.value)} required />
          </label>
          <label className="form-field">
            调度权重
            <input type="number" min="1" max="1000" value={weight} onChange={(event) => setWeight(event.target.value)} required />
            <small>权重越高，在资源条件相同时越优先分配。</small>
          </label>
          <label className="form-field">
            服务器标签
            <input value={labels} onChange={(event) => setLabels(event.target.value)} placeholder="用途=批处理, 环境=生产" />
            <small>使用半角逗号分隔多个“名称=值”标签。</small>
          </label>
          <button type="submit" className="primary-action" disabled={saving}>{saving ? '保存中…' : '保存更改'}</button>
        </form>

        {isAdmin ? <section className="credential-panel" aria-labelledby="credential-title"><div><p className="eyebrow">管理员安全操作</p><h3 id="credential-title">代理凭据</h3><p>轮换会等待新凭据确认连接；紧急吊销会立即断开该节点。</p></div>{securityError ? <div className="form-error" role="alert">{securityError}</div> : null}{securityStatus ? <div className="credential-status" role="status">{securityStatus}</div> : null}{credential ? <div className="one-time-credential"><strong>仅显示一次的新凭据</strong><code>{credential}</code><small>请立即写入代理的安全配置文件，不要通过聊天或工单传递。</small></div> : null}<div className="credential-actions"><button className="secondary-action" type="button" disabled={securityBusy} onClick={() => void rotate()}>轮换代理凭据</button><button className="danger-action" type="button" disabled={securityBusy} onClick={() => setConfirmRevoke(true)}>紧急吊销全部凭据</button></div>{confirmRevoke ? <div className="revoke-confirm" role="alertdialog" aria-labelledby="revoke-title" aria-describedby="revoke-description"><strong id="revoke-title">确认立即断开此节点？</strong><p id="revoke-description">全部现有凭据都会失效，恢复连接前必须重新签发并配置凭据。</p><div><button className="secondary-action" type="button" onClick={() => setConfirmRevoke(false)}>取消</button><button className="danger-action" type="button" disabled={securityBusy} onClick={() => void revoke()}>确认紧急吊销</button></div></div> : null}</section> : null}
      </aside>
    </div>
  )
}

function formatLabels(labels: Record<string, string>) {
  return Object.entries(labels).map(([key, value]) => `${key}=${value}`).join(', ')
}

function parseLabels(value: string) {
  const labels: Record<string, string> = {}
  for (const item of value.split(',')) {
    const [rawKey, ...rawValue] = item.split('=')
    const key = rawKey?.trim()
    if (key) labels[key] = rawValue.join('=').trim()
  }
  return labels
}

function statusLabel(server: ServerView) {
  if (!server.enabled) return '已停用'
  if (server.draining) return '排空中'
  const labels: Record<ServerView['status'], string> = {
    pending: '待连接',
    online: '在线',
    offline: '离线',
    draining: '排空中',
    quarantined: '已隔离',
  }
  return labels[server.status]
}
