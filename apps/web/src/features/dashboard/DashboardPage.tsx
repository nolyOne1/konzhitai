import { useEffect, useState } from 'react'

import { getDashboard, type DashboardData, type ServerView } from '../../api/client'

const emptyDashboard: DashboardData = {
  onlineServers: 0,
  totalServers: 0,
  runningRuns: 0,
  queuedRuns: 0,
  todaySuccessRate: 100,
  servers: [],
  recentEvents: [],
}

export function DashboardPage() {
  const [dashboard, setDashboard] = useState<DashboardData>(emptyDashboard)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')

  useEffect(() => {
    let active = true
    getDashboard()
      .then((data) => {
        if (active) setDashboard(data)
      })
      .catch((reason: unknown) => {
        if (active) setError(reason instanceof Error ? reason.message : '运行数据加载失败')
      })
      .finally(() => {
        if (active) setLoading(false)
      })
    return () => { active = false }
  }, [])

  const metrics = [
    { label: '在线服务器', value: `${dashboard.onlineServers} / ${dashboard.totalServers}`, note: '可接收新任务的执行节点' },
    { label: '运行中', value: String(dashboard.runningRuns), note: '已获得资源租约的任务' },
    { label: '排队任务', value: String(dashboard.queuedRuns), note: '无合适服务器时自动等待' },
    { label: '今日成功率', value: `${formatNumber(dashboard.todaySuccessRate)}%`, note: '今日已结束任务的执行结果' },
  ]

  return (
    <>
      <div className="page-heading">
        <div>
          <p className="eyebrow">实时运行态势</p>
          <h1>运行总览</h1>
          <p>集中查看跨服务器资源、任务负载与脚本同步状态</p>
        </div>
        <a className="primary-action button-link" href="/tasks/new">新建任务</a>
      </div>

      {error && <div className="notice notice-error" role="alert">{error}。已显示安全的空状态。</div>}

      <section className="metric-grid" aria-label="运行指标" aria-busy={loading}>
        {metrics.map((metric) => (
          <article className="metric-card" key={metric.label}>
            <p>{metric.label}</p>
            <strong>{loading ? '—' : metric.value}</strong>
            <small>{metric.note}</small>
          </article>
        ))}
      </section>

      <section className="queue-rule" aria-label="自动排队规则">
        <span className="queue-rule-icon" aria-hidden="true"><svg viewBox="0 0 24 24"><path d="M5 12h14m-5-5 5 5-5 5" /></svg></span>
        <div>
          <strong>队列自动调度已开启</strong>
          <p>当前没有符合标签、运行环境或剩余资源的服务器时，任务保持“排队中”；资源空闲后自动运行。</p>
        </div>
        <span className="rule-badge">运行规则</span>
      </section>

      <div className="dashboard-grid">
        <section className="panel panel-wide" aria-labelledby="server-load-title">
          <PanelHeader title="服务器负载" meta={`${dashboard.onlineServers} 台在线`} />
          <div className="server-load-list">
            {dashboard.servers.length === 0 ? (
              <EmptyPanel message="暂无服务器资源快照" />
            ) : dashboard.servers.slice(0, 5).map((server) => <ServerLoad key={server.id} server={server} />)}
          </div>
        </section>

        <section className="panel" aria-labelledby="live-runs-title">
          <PanelHeader title="实时任务" meta={`${dashboard.runningRuns} 个运行中`} />
          <EmptyPanel message="当前没有运行中的任务" />
          <p className="panel-hint">调度信息会显示“空闲内存最高”、“已缓存脚本版本”或资源不足原因。</p>
        </section>

        <section className="panel" aria-labelledby="sync-title">
          <PanelHeader title="脚本同步" meta="全服务器" />
          <div className="sync-overview">
            <span className="sync-ring" aria-hidden="true">0</span>
            <div><strong>尚无已发布脚本</strong><p>脚本发布后将自动展示同步进度、校验结果和版本漂移。</p></div>
          </div>
        </section>

        <section className="panel panel-wide" aria-labelledby="events-title">
          <PanelHeader title="最近动态" meta="最新 8 条" />
          {dashboard.recentEvents.length === 0 ? (
            <EmptyPanel message="暂无运行动态" />
          ) : (
            <ol className="event-list">
              {dashboard.recentEvents.map((event) => (
                <li key={event.id}><span className="event-dot" aria-hidden="true" /><span>{event.message}</span><time>{formatTime(event.occurredAt)}</time></li>
              ))}
            </ol>
          )}
        </section>
      </div>
    </>
  )
}

function PanelHeader({ title, meta }: { title: string; meta: string }) {
  const id = title === '服务器负载' ? 'server-load-title' : title === '实时任务' ? 'live-runs-title' : title === '脚本同步' ? 'sync-title' : 'events-title'
  return <header className="panel-header"><h2 id={id}>{title}</h2><span>{meta}</span></header>
}

function EmptyPanel({ message }: { message: string }) {
  return <div className="compact-empty"><span aria-hidden="true" />{message}</div>
}

function ServerLoad({ server }: { server: ServerView }) {
  const memoryUsed = server.memoryTotalBytes > 0
    ? ((server.memoryTotalBytes - server.memoryAvailableBytes) / server.memoryTotalBytes) * 100
    : 0
  return (
    <article className="server-load-row">
      <div className="server-identity"><span className={`status-dot status-${server.status}`} aria-hidden="true" /><div><strong>{server.name}</strong><small>{server.cloudProvider || '未分类'} · {server.region || '未设置地域'}</small></div></div>
      <ResourceBar label="CPU" value={server.cpuUsagePercent} />
      <ResourceBar label="内存" value={memoryUsed} />
      <span className="running-count">{server.runningTasks} 件运行中</span>
    </article>
  )
}

function ResourceBar({ label, value }: { label: string; value: number }) {
  const normalized = Math.max(0, Math.min(100, value))
  return <div className="resource-cell"><span>{label}<b>{formatNumber(normalized)}%</b></span><div className="progress-track"><i style={{ width: `${normalized}%` }} /></div></div>
}

function formatNumber(value: number) {
  return Number.isInteger(value) ? String(value) : value.toFixed(1)
}

function formatTime(value: string) {
  return new Intl.DateTimeFormat('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' }).format(new Date(value))
}
