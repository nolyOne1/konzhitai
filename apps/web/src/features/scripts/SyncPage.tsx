import { useCallback, useEffect, useMemo, useRef, useState } from 'react'

import {
  getScripts, getScriptSyncs, retryScriptSync,
  type ScriptSyncState, type ScriptSyncView, type ScriptView,
} from '../../api/client'

const stateLabels: Record<ScriptSyncState, string> = {
  pending: '等待下载', downloading: '下载中', ready: '已就绪', failed: '同步失败', drifted: '发现漂移',
}

interface SyncGroup { script: ScriptView; syncs: ScriptSyncView[]; error: string }

export function SyncPage({ pollIntervalMs = 5000 }: { pollIntervalMs?: number }) {
  const [groups, setGroups] = useState<SyncGroup[]>([])
  const [loading, setLoading] = useState(true)
  const [refreshing, setRefreshing] = useState(false)
  const [retrying, setRetrying] = useState('')
  const [error, setError] = useState('')
  const [message, setMessage] = useState('')
  const activeRef = useRef(true)
  const errorRef = useRef<HTMLDivElement>(null)

  const load = useCallback(async () => {
    setRefreshing(true)
    try {
      const scripts = (await getScripts()).filter((script) => script.currentVersion > 0)
      const nextGroups = await Promise.all(scripts.map(async (script): Promise<SyncGroup> => {
        try {
          const syncs = await getScriptSyncs(script.id)
          return { script, syncs: syncs.filter((item) => item.versionNumber === script.currentVersion), error: '' }
        } catch (reason) {
          return { script, syncs: [], error: reason instanceof Error ? reason.message : '读取同步状态失败' }
        }
      }))
      if (!activeRef.current) return
      setGroups(nextGroups)
      const failed = nextGroups.filter((group) => group.error).length
      setError(failed > 0 ? `${failed} 个脚本的同步状态读取失败，请稍后刷新。` : '')
    } catch (reason) {
      if (activeRef.current) {
        setGroups([])
        setError(reason instanceof Error ? reason.message : '读取脚本同步总览失败')
      }
    } finally {
      if (activeRef.current) { setLoading(false); setRefreshing(false) }
    }
  }, [])

  useEffect(() => {
    activeRef.current = true
    void load()
    return () => { activeRef.current = false }
  }, [load])

  const syncs = useMemo(() => groups.flatMap((group) => group.syncs), [groups])
  const hasActiveSync = syncs.some((item) => item.state === 'pending' || item.state === 'downloading')

  useEffect(() => {
    if (!hasActiveSync) return
    const timer = window.setInterval(() => { void load() }, pollIntervalMs)
    return () => window.clearInterval(timer)
  }, [hasActiveSync, load, pollIntervalMs])

  useEffect(() => { if (error) errorRef.current?.focus() }, [error])

  const counts = {
    scripts: groups.length,
    ready: syncs.filter((item) => item.state === 'ready').length,
    active: syncs.filter((item) => item.state === 'pending' || item.state === 'downloading').length,
    issues: syncs.filter((item) => item.state === 'failed' || item.state === 'drifted').length,
  }

  async function retry(group: SyncGroup, item: ScriptSyncView) {
    setRetrying(item.id); setError(''); setMessage('')
    try {
      await retryScriptSync(group.script.id, item.id)
      if (activeRef.current) setMessage(`${group.script.name}在${item.serverName}的同步请求已重新进入队列。`)
      await load()
    } catch (reason) {
      if (activeRef.current) setError(reason instanceof Error ? reason.message : '重试同步失败')
    } finally {
      if (activeRef.current) setRetrying('')
    }
  }

  return (
    <>
      <div className="page-heading sync-page-heading">
        <div><p className="eyebrow">跨服务器版本分发</p><h1>脚本同步</h1><p>以中央发布版本为唯一来源，查看脚本在各执行服务器上的下载、校验和可用状态。</p></div>
        <div className="heading-actions"><a className="secondary-action button-link" href="/scripts">进入脚本中心</a><button className="primary-action" type="button" disabled={refreshing} onClick={() => void load()}>{refreshing ? '正在刷新…' : '刷新状态'}</button></div>
      </div>

      {error ? <div ref={errorRef} className="notice notice-error" role="alert" tabIndex={-1}>{error}</div> : null}
      {message ? <div className="notice notice-success" role="status">{message}</div> : null}

      <section className="script-summary sync-summary" aria-label="同步统计">
        <Summary label="已发布脚本" value={counts.scripts} /><Summary label="已就绪" value={counts.ready} /><Summary label="同步中" value={counts.active} /><Summary label="异常节点" value={counts.issues} warning={counts.issues > 0} />
      </section>

      <section className="panel sync-overview-panel" aria-labelledby="sync-overview-title">
        <header className="panel-header"><div><h2 id="sync-overview-title">当前版本分发状态</h2><p>只展示每个脚本当前发布版本；旧版本记录仍保留在审计与脚本详情中。</p></div><span>{syncs.length} 条服务器记录</span></header>
        {loading ? <div className="compact-empty" aria-live="polite"><span aria-hidden="true" />正在读取同步状态…</div> : groups.length === 0 ? (
          <div className="large-empty"><span className="empty-script-mark" aria-hidden="true">&lt;/&gt;</span><h3>还没有已发布脚本</h3><p>请先在脚本中心保存并发布版本，再选择分发范围。</p></div>
        ) : (
          <div className="table-scroll"><table className="data-table sync-overview-table"><thead><tr><th>脚本与版本</th><th>执行服务器</th><th>同步状态</th><th>校验与说明</th><th>更新时间</th><th>操作</th></tr></thead><tbody>{groups.flatMap((group) => group.syncs.length > 0 ? group.syncs.map((item) => (
            <tr key={item.id}>
              <td data-label="脚本与版本"><a className="script-name-link" href={`/scripts/${group.script.id}`}><strong>{group.script.name}</strong><span>版本 {group.script.currentVersion} · {runtimeLabel(group.script.runtime)}</span></a></td>
              <td data-label="执行服务器"><strong>{item.serverName}</strong><code className="block-code">{shortID(item.serverId)}</code></td>
              <td data-label="同步状态"><span className={`sync-state sync-state-${item.state}`}><i aria-hidden="true" />{stateLabels[item.state]}</span></td>
              <td data-label="校验与说明"><span>{item.errorMessage || syncDescription(item.state)}</span><code className="block-code">{shortHash(item.artifactSha256)}</code></td>
              <td data-label="更新时间"><time>{formatDate(item.updatedAt)}</time></td>
              <td data-label="操作">{item.state === 'failed' || item.state === 'drifted' ? <button className="secondary-action sync-retry-button" type="button" disabled={retrying === item.id} aria-label={`重试${group.script.name}在${item.serverName}的同步`} onClick={() => void retry(group, item)}>{retrying === item.id ? '重试中…' : '立即重试'}</button> : <span className="sync-no-action">无需操作</span>}</td>
            </tr>
          )) : [(
            <tr key={`${group.script.id}-empty`}><td data-label="脚本与版本"><a className="script-name-link" href={`/scripts/${group.script.id}`}><strong>{group.script.name}</strong><span>版本 {group.script.currentVersion} · {runtimeLabel(group.script.runtime)}</span></a></td><td data-label="执行服务器">暂无分发目标</td><td data-label="同步状态"><span className="sync-state"><i aria-hidden="true" />等待目标</span></td><td data-label="校验与说明"><span>{group.error || '当前没有符合运行环境、标签或发布范围的服务器。'}</span></td><td data-label="更新时间">—</td><td data-label="操作"><span className="sync-no-action">无需操作</span></td></tr>
          )])}</tbody></table></div>
        )}
      </section>
    </>
  )
}

function Summary({ label, value, warning = false }: { label: string; value: number; warning?: boolean }) { return <div className={warning ? 'is-warning' : ''}><span>{label}</span><strong>{value}</strong></div> }
function syncDescription(state: ScriptSyncState) { return ({ pending: '等待代理领取同步任务', downloading: '代理正在下载并校验脚本包', ready: '内容校验一致，可用于任务执行', failed: '同步未完成，可人工重试', drifted: '服务器内容与中央版本不一致' } as const)[state] }
function runtimeLabel(runtime: string) { return ({ bash: 'Shell / Bash', python3: 'Python 3', node: 'Node.js', powershell: 'PowerShell' } as Record<string, string>)[runtime] || runtime }
function shortHash(value: string) { return value ? `SHA-256 ${value.slice(0, 10)}…` : '等待校验' }
function shortID(value: string) { return value.length > 12 ? `${value.slice(0, 8)}…` : value }
function formatDate(value: string) { return new Intl.DateTimeFormat('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' }).format(new Date(value)) }
