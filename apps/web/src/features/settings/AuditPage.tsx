import { useEffect, useMemo, useRef, useState } from 'react'

import { getAlerts, getAuditEvents, type AuditEvent, type SystemAlert } from '../../api/client'
import { AlertsPanel } from './AlertsPanel'

const actionLabels: Record<string, string> = {
  'auth.login': '登录控制台', 'script.create': '创建脚本', 'script.publish': '发布脚本',
  'script.rollback': '回滚脚本', 'script.sync.retry': '重试脚本同步', 'task.create': '创建任务',
  'task.run': '手动执行任务', 'task.enabled.update': '切换任务状态', 'run.cancel': '终止运行',
  'run.retry': '重新执行', 'secret.create': '创建敏感参数', 'member.roles.update': '调整成员角色',
  'server.credential.rotate': '轮换代理凭据', 'server.credential.revoke': '吊销代理凭据',
  'alert.acknowledge': '确认系统告警',
  'operations.feishu.update': '更新飞书通知', 'operations.feishu.disable': '停用飞书通知',
  'operations.feishu.test': '发送飞书测试消息',
  'operations.backup.request': '请求立即备份', 'operations.verification.request': '请求恢复校验',
}

export function AuditPage() {
  const [events, setEvents] = useState<AuditEvent[]>([])
  const [alerts, setAlerts] = useState<SystemAlert[]>([])
  const [filter, setFilter] = useState('')
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const errorRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    let active = true
    Promise.all([getAuditEvents(), getAlerts()]).then(([auditEvents, systemAlerts]) => {
      if (!active) return
      setEvents(auditEvents); setAlerts(systemAlerts)
    }).catch((reason: unknown) => { if (active) setError(reason instanceof Error ? reason.message : '读取安全设置失败') })
      .finally(() => { if (active) setLoading(false) })
    return () => { active = false }
  }, [])

  useEffect(() => { if (error) errorRef.current?.focus() }, [error])

  const visibleEvents = useMemo(() => events.filter((event) => !filter || event.action === filter), [events, filter])

  return <><div className="page-heading"><div><p className="eyebrow">安全与可追溯性</p><h1>系统设置</h1><p>集中处理运行告警，并查看所有关键操作的只追加审计记录。</p></div></div>{error ? <div ref={errorRef} className="notice notice-error" role="alert" tabIndex={-1}>{error}</div> : null}<AlertsPanel alerts={alerts} onChange={setAlerts} /><section className="panel settings-panel audit-panel" aria-labelledby="audit-title"><header className="panel-header"><div><h2 id="audit-title">审计日志</h2><p>历史记录只允许追加，禁止修改和删除。</p></div><label className="compact-filter"><span>操作类型</span><select value={filter} onChange={(event) => setFilter(event.target.value)}><option value="">全部操作</option>{Array.from(new Set(events.map((event) => event.action))).map((action) => <option key={action} value={action}>{actionLabel(action)}</option>)}</select></label></header>{loading ? <div className="compact-empty" aria-live="polite"><span aria-hidden="true" />正在读取审计日志…</div> : visibleEvents.length === 0 ? <div className="large-empty"><span className="audit-mark" aria-hidden="true" /><h3>暂无审计记录</h3><p>登录、发布、执行和安全配置变更会自动记录在这里。</p></div> : <div className="table-scroll"><table className="data-table settings-table"><thead><tr><th>时间</th><th>操作</th><th>操作者</th><th>目标</th><th>来源地址</th></tr></thead><tbody>{visibleEvents.map((event) => <tr key={event.id}><td data-label="时间">{formatTime(event.createdAt)}</td><td data-label="操作"><strong>{actionLabel(event.action)}</strong><span className="cell-note">{event.action}</span></td><td data-label="操作者"><code>{event.actorId || '系统'}</code></td><td data-label="目标"><span>{event.targetType}</span><code className="block-code">{event.targetId}</code></td><td data-label="来源地址"><code>{event.ipAddress || '内部服务'}</code></td></tr>)}</tbody></table></div>}</section></>
}

function actionLabel(action: string) { return actionLabels[action] || action }
function formatTime(value: string) { return new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value)) }
