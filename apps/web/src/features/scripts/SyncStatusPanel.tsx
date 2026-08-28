import { useCallback, useEffect, useMemo, useState } from 'react'

import { getScriptSyncs, retryScriptSync, type ScriptSyncState, type ScriptSyncView } from '../../api/client'

const stateLabels: Record<ScriptSyncState, string> = {
  pending: '等待下载',
  downloading: '下载中',
  ready: '已就绪',
  failed: '同步失败',
  drifted: '发现漂移',
}

export function SyncStatusPanel({ scriptId, refreshKey = 0 }: { scriptId: string; refreshKey?: string | number }) {
  const [items, setItems] = useState<ScriptSyncView[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [retrying, setRetrying] = useState('')

  const load = useCallback(async () => {
    try {
      setError('')
      setItems(await getScriptSyncs(scriptId))
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '读取同步状态失败')
    } finally {
      setLoading(false)
    }
  }, [scriptId])

  useEffect(() => {
    setLoading(true)
    void load()
  }, [load, refreshKey])

  const latest = useMemo(() => {
    const version = items.reduce((maximum, item) => Math.max(maximum, item.versionNumber), 0)
    return items.filter((item) => item.versionNumber === version)
  }, [items])
  const ready = latest.filter((item) => item.state === 'ready').length

  async function retry(item: ScriptSyncView) {
    setRetrying(item.id)
    setError('')
    try {
      await retryScriptSync(scriptId, item.id)
      await load()
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '重试同步失败')
    } finally {
      setRetrying('')
    }
  }

  return (
    <section className="panel sync-panel" aria-labelledby="sync-status-title">
      <header className="panel-header">
        <div><h2 id="sync-status-title">服务器同步状态</h2><p>服务器仅在脚本包校验通过后切换版本；漂移节点会自动重新同步。</p></div>
        <span>{latest.length === 0 ? '暂无分发目标' : `${ready} / ${latest.length} 已就绪`}</span>
      </header>
      {error && <div className="sync-panel-error" role="status">{error}</div>}
      {loading ? <div className="sync-panel-empty" aria-live="polite">正在读取同步状态…</div> : latest.length === 0 ? (
        <div className="sync-panel-empty">当前版本按需分发，或暂时没有符合运行环境和标签的在线服务器。</div>
      ) : (
        <ul className="sync-list">
          {latest.map((item) => (
            <li key={item.id}>
              <div className="sync-server"><strong>{item.serverName}</strong><span>版本 {item.versionNumber} · {shortHash(item.artifactSha256)}</span></div>
              <span className={`sync-state sync-state-${item.state}`}><i aria-hidden="true" />{stateLabels[item.state]}</span>
              <div className="sync-message"><span>{item.errorMessage || (item.state === 'ready' ? '内容校验一致，可用于任务执行' : '等待代理返回同步结果')}</span><small>{formatDate(item.updatedAt)}</small></div>
              {(item.state === 'failed' || item.state === 'drifted') && <button type="button" disabled={retrying === item.id} aria-label={`重试${item.serverName}`} onClick={() => void retry(item)}>{retrying === item.id ? '重试中' : '立即重试'}</button>}
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}

function shortHash(value: string) {
  return value ? `${value.slice(0, 8)}…` : '等待校验'
}

function formatDate(value: string) {
  return new Intl.DateTimeFormat('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' }).format(new Date(value))
}
