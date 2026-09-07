import { useCallback, useEffect, useMemo, useRef, useState } from 'react'

import { getAgentReleases, getAgentUpgradePlan, getAgentUpgradePlans, getServers, getSession, recommendAgentRelease, withdrawAgentRelease, type AgentRelease, type AgentUpgradePlan, type ServerView } from '../../api/client'
import { AgentUpgradeDialog } from './AgentUpgradeDialog'
import { AgentUpgradePlanPanel } from './AgentUpgradePlanPanel'
import { ServerSectionTabs } from './ServerSectionTabs'

export function AgentUpgradesPage() {
  const [releases, setReleases] = useState<AgentRelease[]>([])
  const [servers, setServers] = useState<ServerView[]>([])
  const [plans, setPlans] = useState<AgentUpgradePlan[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [busyRelease, setBusyRelease] = useState('')
  const [showDialog, setShowDialog] = useState(false)
  const [isAdmin, setIsAdmin] = useState(false)
  const createButtonRef = useRef<HTMLButtonElement>(null)

  const load = useCallback(async () => {
    setLoading(true); setError('')
    try {
      const [nextReleases, nextServers, nextPlans, session] = await Promise.all([getAgentReleases(), getServers(), getAgentUpgradePlans(), getSession()])
      setReleases(nextReleases); setServers(nextServers); setPlans(nextPlans)
      setIsAdmin(session.roles.includes('admin'))
    } catch (reason) { setError(reason instanceof Error ? reason.message : '代理升级数据加载失败，请重试。') }
    finally { setLoading(false) }
  }, [])
  useEffect(() => { void load() }, [load])

  const activePlan = plans.find((plan) => plan.status === 'pending' || plan.status === 'running' || plan.status === 'paused')
  useEffect(() => {
    if (!activePlan) return
    const timer = window.setInterval(() => void getAgentUpgradePlan(activePlan.id).then((updated) => setPlans((items) => items.map((item) => item.id === updated.id ? updated : item))).catch(() => undefined), 5000)
    return () => window.clearInterval(timer)
  }, [activePlan?.id, activePlan?.status])

  const names = useMemo(() => Object.fromEntries(servers.map((server) => [server.id, server.name])), [servers])
  const recommended = releases.find((release) => release.recommended)
  const upgradingStatuses = ['waiting', 'draining', 'downloading', 'verifying', 'installing', 'reconnecting', 'health_checking', 'rolling_back']
  const upgrading = servers.filter((server) => upgradingStatuses.includes(server.upgradeStatus ?? '')).length
  const exceptions = servers.filter((server) => server.upgradeStatus === 'manual_intervention').length
  const pending = servers.filter((server) => recommended && server.agentVersion && server.agentVersion !== recommended.version && !upgradingStatuses.includes(server.upgradeStatus ?? '') && server.upgradeStatus !== 'manual_intervention').length
  const updatePlan = (plan: AgentUpgradePlan) => setPlans((items) => items.some((item) => item.id === plan.id) ? items.map((item) => item.id === plan.id ? plan : item) : [plan, ...items])
  async function mutateRelease(id: string, operation: () => Promise<void>) { setBusyRelease(id); setError(''); try { await operation(); setReleases(await getAgentReleases()) } catch (reason) { setError(reason instanceof Error ? reason.message : '更新代理版本失败') } finally { setBusyRelease('') } }
  function closeDialog() { setShowDialog(false); window.setTimeout(() => createButtonRef.current?.focus(), 0) }

  return <>
    <div className="page-heading"><div><p className="eyebrow">多服务器代理运维</p><h1>代理升级</h1><p>集中盘点代理版本，首批单节点验证后自动分批升级。</p></div>{isAdmin ? <button ref={createButtonRef} type="button" className="primary-action" disabled={Boolean(activePlan) || loading} onClick={() => setShowDialog(true)}>创建升级计划</button> : null}</div>
    <ServerSectionTabs current="upgrades" />
    {error ? <div className="notice notice-error" role="alert"><span>{error}</span><button type="button" className="secondary-action" onClick={() => void load()}>重新加载</button></div> : null}
    <section className="server-summary upgrade-summary" aria-label="升级概况"><div><span>推荐版本</span><strong>{loading ? '—' : recommended?.version || '未设置'}</strong></div><div><span>服务器总数</span><strong>{loading ? '—' : servers.length}</strong></div><div><span>待升级</span><strong>{loading ? '—' : pending}</strong></div><div><span>升级中</span><strong>{loading ? '—' : upgrading}</strong></div><div><span>异常</span><strong>{loading ? '—' : exceptions}</strong></div></section>
    {!loading && !isAdmin ? <p className="backup-readonly-note">当前账号可以查看升级盘点和进度；计划控制仅限管理员。</p> : null}
    {activePlan ? <AgentUpgradePlanPanel plan={activePlan} serverNames={names} onUpdated={updatePlan} readOnly={!isAdmin} /> : <section className="panel upgrade-empty" aria-busy={loading}><span className="empty-server-mark" aria-hidden="true" /><div><h2>当前没有进行中的升级计划</h2><p>创建计划后，云令会先排空首台服务器并验证代理健康状态。</p></div></section>}
    <section className="panel release-panel" aria-labelledby="release-list-title"><header className="panel-header"><div><h2 id="release-list-title">代理版本</h2><p>推荐版本用于版本盘点；已撤回版本不能创建新升级计划。</p></div><span>{releases.length} 个版本</span></header><div className="table-scroll"><table className="data-table"><thead><tr><th>版本</th><th>状态</th><th>平台</th><th>发布说明</th><th>发布时间</th><th><span className="sr-only">操作</span></th></tr></thead><tbody>{releases.map((release) => <tr key={release.id}><td data-label="版本"><strong>{release.version}</strong>{release.recommended ? <small className="recommended-mark">推荐版本</small> : null}</td><td data-label="状态">{release.status === 'available' ? '可用' : '已撤回'}</td><td data-label="平台">{release.artifacts.map((item) => `${item.os}/${item.arch}`).join('、') || '未提供'}</td><td data-label="发布说明">{release.releaseNotes || '暂无说明'}</td><td data-label="发布时间"><time dateTime={release.createdAt}>{formatDate(release.createdAt)}</time></td><td data-label="操作"><div className="row-actions">{isAdmin && release.status === 'available' && !release.recommended ? <><button type="button" disabled={Boolean(busyRelease)} onClick={() => void mutateRelease(release.id, () => recommendAgentRelease(release.id))}>设为推荐</button><button type="button" className="danger-text" disabled={Boolean(busyRelease)} onClick={() => void mutateRelease(release.id, () => withdrawAgentRelease(release.id))}>{busyRelease === release.id ? '处理中…' : '撤回版本'}</button></> : null}</div></td></tr>)}</tbody></table>{!loading && releases.length === 0 ? <div className="table-empty">还没有导入代理版本</div> : null}</div></section>
    {plans.filter((plan) => plan !== activePlan).length ? <section className="upgrade-history" aria-labelledby="upgrade-history-title"><h2 id="upgrade-history-title">升级历史</h2>{plans.filter((plan) => plan !== activePlan).map((plan) => <AgentUpgradePlanPanel key={plan.id} plan={plan} serverNames={names} onUpdated={updatePlan} readOnly={!isAdmin || Boolean(activePlan)} />)}</section> : null}
    {showDialog ? <AgentUpgradeDialog releases={releases} servers={servers} onClose={closeDialog} onCreated={(plan) => { updatePlan(plan); closeDialog() }} /> : null}
  </>
}

function formatDate(value: string) { if (!value) return '时间未知'; return new Intl.DateTimeFormat('zh-CN', { year: 'numeric', month: '2-digit', day: '2-digit' }).format(new Date(value)) }
