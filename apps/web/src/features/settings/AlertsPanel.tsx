import { useEffect, useRef, useState } from 'react'

import { acknowledgeAlert, type SystemAlert } from '../../api/client'

export function AlertsPanel({ alerts, onChange }: { alerts: SystemAlert[]; onChange: (alerts: SystemAlert[]) => void }) {
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const errorRef = useRef<HTMLDivElement>(null)

  useEffect(() => { if (error) errorRef.current?.focus() }, [error])

  async function acknowledge(item: SystemAlert) {
    setBusy(item.id); setError('')
    try {
      await acknowledgeAlert(item.id)
      onChange(alerts.map((candidate) => candidate.id === item.id ? { ...candidate, status: 'acknowledged' } : candidate))
    } catch (reason) { setError(reason instanceof Error ? reason.message : '确认告警失败，请重试') }
    finally { setBusy('') }
  }

  const openCount = alerts.filter((item) => item.status === 'open').length
  return <section className="panel alert-panel" aria-labelledby="alerts-title"><header className="panel-header"><div><h2 id="alerts-title">系统告警</h2><p>五分钟内同一资源与错误码会自动合并计数。</p></div><span>{openCount} 条待处理</span></header>{error ? <div ref={errorRef} className="inline-panel-error" role="alert" tabIndex={-1}>{error}</div> : null}{alerts.length === 0 ? <div className="compact-empty"><span aria-hidden="true" />当前没有系统告警</div> : <ul className="alert-list">{alerts.map((item) => <li key={item.id} className={`alert-item severity-${item.severity}`}><span className="alert-severity-mark" aria-hidden="true" /><div className="alert-copy"><div><strong>{item.title}</strong><span className="alert-count">合并 {item.occurrences} 次</span></div><p>{item.message || `${item.resourceType} · ${item.resourceId}`}</p><small>{formatTime(item.lastOccurredAt)} · {item.code}</small></div>{item.status === 'open' ? <button className="secondary-action" type="button" disabled={busy === item.id} aria-label={`确认${item.title}告警`} onClick={() => void acknowledge(item)}>{busy === item.id ? '处理中…' : '确认'}</button> : <span className="status-badge status-badge-online"><i aria-hidden="true" />已确认</span>}</li>)}</ul>}</section>
}

function formatTime(value: string) { return new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value)) }
