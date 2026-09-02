import { useEffect, useMemo, useRef, useState } from 'react'

import { getServers, getSession, revokeServerCredentials, rotateServerCredential, updateServer, type ServerView, type UpdateServerInput } from '../../api/client'
import { ServerDrawer } from './ServerDrawer'
import { ServerEnrollmentDialog } from './ServerEnrollmentDialog'

export function ServersPage() {
  const [servers, setServers] = useState<ServerView[]>([])
  const [selectedID, setSelectedID] = useState<string | null>(null)
  const [pendingID, setPendingID] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [isAdmin, setIsAdmin] = useState(false)
  const [sessionReady, setSessionReady] = useState(false)
  const [sessionError, setSessionError] = useState('')
  const [securityBusy, setSecurityBusy] = useState(false)
  const [showEnrollment, setShowEnrollment] = useState(false)
  const enrollmentButtonRef = useRef<HTMLButtonElement>(null)
  const sessionRequestRef = useRef(0)

  useEffect(() => {
    let active = true
    getServers()
      .then((items) => { if (active) setServers(items) })
      .catch((reason: unknown) => { if (active) setError(reason instanceof Error ? reason.message : '服务器加载失败') })
      .finally(() => { if (active) setLoading(false) })
    return () => { active = false }
  }, [])

  useEffect(() => {
    void loadSession()
    return () => { sessionRequestRef.current += 1 }
  }, [])

  const selected = useMemo(() => servers.find((server) => server.id === selectedID) ?? null, [selectedID, servers])

  async function saveServer(server: ServerView, input: UpdateServerInput) {
    setPendingID(server.id)
    setError('')
    try {
      const updated = await updateServer(server.id, input)
      setServers((items) => items.map((item) => item.id === updated.id ? updated : item))
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '更新服务器失败')
    } finally {
      setPendingID(null)
    }
  }

  async function rotateCredential(server: ServerView) {
    setSecurityBusy(true)
    try { return (await rotateServerCredential(server.id)).credential }
    finally { setSecurityBusy(false) }
  }

  async function revokeCredentials(server: ServerView) {
    setSecurityBusy(true)
    try {
      await revokeServerCredentials(server.id)
      setServers((items) => items.map((item) => item.id === server.id ? { ...item, status: 'offline' } : item))
    } finally { setSecurityBusy(false) }
  }

  async function loadSession() {
    const requestID = sessionRequestRef.current + 1
    sessionRequestRef.current = requestID
    setSessionReady(false)
    setSessionError('')
    setIsAdmin(false)
    try {
      const session = await getSession()
      if (sessionRequestRef.current === requestID) setIsAdmin(session.roles.includes('admin'))
    } catch {
      if (sessionRequestRef.current === requestID) setSessionError('权限信息加载失败，请重试。')
    } finally {
      if (sessionRequestRef.current === requestID) setSessionReady(true)
    }
  }

  function closeEnrollment() {
    setShowEnrollment(false)
    window.setTimeout(() => enrollmentButtonRef.current?.focus(), 0)
  }

  return (
    <>
      <div className="page-heading">
        <div>
          <p className="eyebrow">多云执行资源</p>
          <h1>服务器</h1>
          <p>查看代理连接、实时资源和调度可用性</p>
        </div>
        <div className="server-heading-action">
          <button ref={enrollmentButtonRef} type="button" className="primary-action" aria-busy={!sessionReady} aria-describedby={sessionError ? 'enrollment-permission-error' : sessionReady && !isAdmin ? 'enrollment-permission-note' : undefined} disabled={!sessionReady || !isAdmin} onClick={() => setShowEnrollment(true)}>接入服务器</button>
          {sessionError ? <div className="server-permission-error" id="enrollment-permission-error" role="alert"><span>{sessionError}</span><button type="button" className="secondary-action" onClick={() => void loadSession()}>重试权限检查</button></div> : sessionReady && !isAdmin ? <small id="enrollment-permission-note">仅管理员可接入新服务器</small> : null}
        </div>
      </div>

      {error && <div className="notice notice-error" role="alert">{error}</div>}

      <section className="server-summary" aria-label="服务器概况">
        <div><span>服务器总数</span><strong>{loading ? '—' : servers.length}</strong></div>
        <div><span>可调度</span><strong>{loading ? '—' : servers.filter((server) => server.enabled && !server.draining && server.status === 'online').length}</strong></div>
        <div><span>排空中</span><strong>{loading ? '—' : servers.filter((server) => server.draining).length}</strong></div>
        <div><span>已停用</span><strong>{loading ? '—' : servers.filter((server) => !server.enabled).length}</strong></div>
      </section>

      <section className="panel server-panel" aria-labelledby="server-list-title" aria-busy={loading}>
        <header className="panel-header server-list-header">
          <div><h2 id="server-list-title">服务器列表</h2><p>排空不会终止当前任务；停用会断开代理并阻止新连接。</p></div>
          <span>{servers.length} 台</span>
        </header>
        {servers.length === 0 && !loading ? (
          <div className="large-empty"><span className="empty-server-mark" aria-hidden="true" /><h3>还没有接入服务器</h3><p>创建一次性注册令牌，在执行服务器安装云令代理即可接入。</p></div>
        ) : (
          <div className="table-scroll">
            <table className="data-table">
              <thead><tr><th>服务器</th><th>状态</th><th>CPU</th><th>可用内存</th><th>运行任务</th><th>标签</th><th><span className="sr-only">操作</span></th></tr></thead>
              <tbody>
                {servers.map((server) => (
                  <tr key={server.id}>
                    <td data-label="服务器"><button type="button" className="server-name-button" aria-label={`查看${server.name}详情`} onClick={() => setSelectedID(server.id)}><strong>{server.name}</strong><span>{server.cloudProvider || '未分类'} · {server.region || '未设置地域'}</span></button></td>
                    <td data-label="状态"><StatusBadge server={server} /></td>
                    <td data-label="CPU"><ResourceValue value={`${formatNumber(server.cpuUsagePercent)}%`} percent={server.cpuUsagePercent} /></td>
                    <td data-label="可用内存"><ResourceValue value={formatBytes(server.memoryAvailableBytes)} percent={percentage(server.memoryTotalBytes - server.memoryAvailableBytes, server.memoryTotalBytes)} /></td>
                    <td data-label="运行任务"><strong>{server.runningTasks}</strong><span className="cell-muted"> / 上限待配置</span></td>
                    <td data-label="标签"><div className="tag-list">{Object.entries(server.labels).map(([key, value]) => <span key={key}>{key}：{value}</span>)}</div></td>
                    <td data-label="操作"><div className="row-actions"><button type="button" disabled={pendingID === server.id || !server.enabled} onClick={() => void saveServer(server, { draining: !server.draining })}>{server.draining ? '取消排空' : '排空'}</button><button type="button" className={server.enabled ? 'danger-text' : ''} disabled={pendingID === server.id} onClick={() => void saveServer(server, { enabled: !server.enabled })}>{server.enabled ? '停用' : '启用'}</button></div></td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>

      {selected && <ServerDrawer server={selected} saving={pendingID === selected.id} securityBusy={securityBusy} isAdmin={isAdmin} onClose={() => setSelectedID(null)} onSave={(input) => saveServer(selected, input)} onRotate={() => rotateCredential(selected)} onRevoke={() => revokeCredentials(selected)} />}
      {showEnrollment && <ServerEnrollmentDialog controlUrl={window.location.origin} onClose={closeEnrollment} />}
    </>
  )
}

function StatusBadge({ server }: { server: ServerView }) {
  const state = !server.enabled ? 'disabled' : server.draining ? 'draining' : server.status
  const labels: Record<string, string> = { disabled: '已停用', draining: '排空中', pending: '待连接', online: '在线', offline: '离线', quarantined: '已隔离' }
  return <span className={`status-badge status-badge-${state}`}><i aria-hidden="true" />{labels[state]}</span>
}

function ResourceValue({ value, percent }: { value: string; percent: number }) {
  const normalized = Math.max(0, Math.min(100, percent))
  return <div className="table-resource"><strong>{value}</strong><span className="progress-track"><i style={{ width: `${normalized}%` }} /></span></div>
}

function percentage(part: number, total: number) {
  return total > 0 ? (part / total) * 100 : 0
}

function formatBytes(value: number) {
  if (value <= 0) return '0 GB'
  return `${(value / 1073741824).toFixed(value >= 10 * 1073741824 ? 0 : 1)} GB`
}

function formatNumber(value: number) {
  return Number.isInteger(value) ? String(value) : value.toFixed(1)
}
