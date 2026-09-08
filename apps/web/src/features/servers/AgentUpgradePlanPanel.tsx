import { useEffect, useRef, useState } from 'react'

import { cancelAgentUpgradePlan, getAgentUpgradePlan, pauseAgentUpgradePlan, resumeAgentUpgradePlan, retryAgentUpgradeTarget, rollbackAgentUpgradeTarget, type AgentUpgradePlan } from '../../api/client'

interface AgentUpgradePlanPanelProps {
  plan: AgentUpgradePlan
  serverNames?: Record<string, string>
  onUpdated: (plan: AgentUpgradePlan) => void
  readOnly?: boolean
}

const targetLabels: Record<string, string> = {
  accepted: '已接受升级指令', failed: '执行失败', waiting: '等待进入批次', draining: '等待任务结束', downloading: '下载安装包', verifying: '校验安装包',
  installing: '安装新版本', reconnecting: '等待代理重连', health_checking: '健康验证', succeeded: '升级成功',
  rolling_back: '正在回滚', rolled_back: '已回滚', manual_intervention: '需要人工处理', cancelled: '已取消',
}
const planLabels: Record<string, string> = { pending: '等待启动', running: '进行中', paused: '已暂停', succeeded: '已完成', cancelled: '已取消' }

export function AgentUpgradePlanPanel({ plan, serverNames = {}, onUpdated, readOnly = false }: AgentUpgradePlanPanelProps) {
  const [current, setCurrent] = useState(plan)
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const [confirmation, setConfirmation] = useState<{ kind: 'cancel' | 'rollback'; targetID?: string } | null>(null)
  const confirmationRef = useRef<HTMLElement>(null)
  const confirmationTriggerRef = useRef<HTMLElement | null>(null)
  useEffect(() => setCurrent(plan), [plan])
  useEffect(() => {
    if (!confirmation) return
    const dialog = confirmationRef.current
    const trigger = confirmationTriggerRef.current
    const keydown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault()
        setConfirmation(null)
        return
      }
      if (event.key !== 'Tab' || !dialog) return
      const focusable = [...dialog.querySelectorAll<HTMLElement>('button:not(:disabled), [href], input:not(:disabled), select:not(:disabled), textarea:not(:disabled), [tabindex]:not([tabindex="-1"])')]
      if (!focusable.length) return
      const first = focusable[0]
      const last = focusable[focusable.length - 1]
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault(); last.focus()
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault(); first.focus()
      }
    }
    document.addEventListener('keydown', keydown)
    return () => {
      document.removeEventListener('keydown', keydown)
      trigger?.focus()
    }
  }, [confirmation])

  function requestConfirmation(choice: { kind: 'cancel' | 'rollback'; targetID?: string }) {
    confirmationTriggerRef.current = document.activeElement as HTMLElement | null
    setConfirmation(choice)
  }

  async function run(key: string, operation: () => Promise<AgentUpgradePlan>) {
    if (busy) return
    setBusy(key); setError('')
    try {
      const result = await operation()
      setCurrent(result)
      onUpdated(result)
      const refreshed = await getAgentUpgradePlan(current.id)
      setCurrent(refreshed)
      onUpdated(refreshed)
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '升级操作失败，请重试。')
    } finally { setBusy('') }
  }

  function confirmImpact() {
    if (!confirmation) return
    const choice = confirmation
    setConfirmation(null)
    if (choice.kind === 'cancel') {
      void run('cancel', () => cancelAgentUpgradePlan(current.id))
      return
    }
    const targetID = choice.targetID
    if (targetID) void run(`rollback-${targetID}`, () => rollbackAgentUpgradeTarget(current.id, targetID))
  }

  const completed = current.targets.filter((target) => ['succeeded', 'rolled_back', 'cancelled'].includes(target.status)).length
  const progress = current.targets.length ? Math.round(completed * 100 / current.targets.length) : 0
  return (
    <section className={`panel upgrade-plan-panel status-${current.status}`} aria-labelledby={`upgrade-plan-${current.id}`}>
      <header className="panel-header upgrade-plan-header">
        <div><p className="eyebrow">目标版本 {current.targetVersion}</p><h2 id={`upgrade-plan-${current.id}`}>{readOnly ? '历史升级计划' : '当前升级计划'}</h2><p>第 {current.currentBatch} 批 · {current.targets.length} 台服务器</p></div>
        <span className={`plan-status plan-status-${current.status}`}><i aria-hidden="true" />{planLabels[current.status] ?? current.status}</span>
      </header>
      <div className="upgrade-progress" aria-label={`升级完成度 ${progress}%`}><span><i style={{ width: `${progress}%` }} /></span><strong>{completed} / {current.targets.length}</strong></div>
      {current.pauseReason ? <div className="notice notice-warning">暂停原因：{current.pauseReason}</div> : null}
      {error ? <div className="notice notice-error" role="alert">{error}</div> : null}
      {!readOnly ? <div className="upgrade-plan-actions">
        {current.status === 'running' ? <button type="button" className="secondary-action" disabled={Boolean(busy)} onClick={() => void run('pause', () => pauseAgentUpgradePlan(current.id, '管理员手动暂停'))}>{busy === 'pause' ? '处理中…' : '暂停计划'}</button> : null}
        {current.status === 'paused' && !current.cancelRequested ? <button type="button" className="primary-action" disabled={Boolean(busy)} onClick={() => void run('resume', () => resumeAgentUpgradePlan(current.id))}>{busy === 'resume' ? '处理中…' : '继续计划'}</button> : null}
        {(current.status === 'running' || current.status === 'paused') && !current.cancelRequested ? <button type="button" className="danger-action" disabled={Boolean(busy)} onClick={() => requestConfirmation({ kind: 'cancel' })}>取消计划</button> : null}
      </div> : null}
      <div className="table-scroll"><table className="data-table upgrade-target-table"><thead><tr><th>服务器</th><th>批次</th><th>版本</th><th>当前阶段</th><th>尝试</th><th><span className="sr-only">操作</span></th></tr></thead><tbody>{current.targets.map((target) => <tr key={target.id}>
        <td data-label="服务器"><strong>{target.serverName || serverNames[target.serverId] || target.serverId}</strong>{target.errorMessage ? <small className="target-error">{safeMessage(target.errorMessage)}</small> : null}</td>
        <td data-label="批次">第 {target.batchNumber} 批</td>
        <td data-label="版本">{target.sourceVersion || '未知'} → {target.targetVersion}</td>
        <td data-label="当前阶段"><span className={`target-stage stage-${target.status}`}><i aria-hidden="true" />{targetLabels[target.status] ?? '状态未知'}</span></td>
        <td data-label="尝试">{target.attempts}</td>
        <td data-label="操作"><div className="row-actions">{!readOnly && ['succeeded', 'cancelled'].includes(current.status) && target.status === 'succeeded' ? <button type="button" disabled={Boolean(busy)} onClick={() => requestConfirmation({ kind: 'rollback', targetID: target.id })}>{busy === `rollback-${target.id}` ? '处理中…' : '回滚此节点'}</button> : null}{!readOnly && ['rolled_back', 'manual_intervention'].includes(target.status) ? <button type="button" disabled={Boolean(busy)} onClick={() => void run(`retry-${target.id}`, () => retryAgentUpgradeTarget(current.id, target.id))}>{busy === `retry-${target.id}` ? '处理中…' : '重试此节点'}</button> : null}</div></td>
      </tr>)}</tbody></table></div>
      {current.events.length ? <section className="upgrade-events" aria-label="升级事件"><h3>最近事件</h3><ol>{[...current.events].reverse().map((event) => <li key={event.id}><time dateTime={event.occurredAt}>{formatTime(event.occurredAt)}</time><div><strong>{targetLabels[event.stage] ?? '状态更新'}</strong><p>{safeMessage(event.message)}</p></div></li>)}</ol></section> : null}
      {confirmation ? <div className="drawer-backdrop centered-dialog"><section ref={confirmationRef} className="console-dialog" role="alertdialog" aria-modal="true" aria-labelledby={`upgrade-confirm-${current.id}`}><header className="drawer-header"><div><p className="eyebrow">确认影响范围</p><h3 id={`upgrade-confirm-${current.id}`}>{confirmation.kind === 'cancel' ? '确认取消升级计划？' : '确认回滚此节点？'}</h3><p>{confirmation.kind === 'cancel' ? `将取消 ${current.targets.filter((target) => target.status === 'waiting').length} 台尚未开始的服务器；已开始节点会继续完成当前闭环。` : '该服务器会保持排空，恢复升级前版本并重新连接；其他已成功批次不受影响。'}</p></div></header><footer className="dialog-actions"><button autoFocus type="button" className="secondary-action" onClick={() => setConfirmation(null)}>返回</button><button type="button" className="danger-action" disabled={Boolean(busy)} onClick={confirmImpact}>{confirmation.kind === 'cancel' ? '确认取消计划' : '确认回滚节点'}</button></footer></section></div> : null}
    </section>
  )
}

function safeMessage(value: string) {
  const firstLine = value.split(/\r?\n/, 1)[0].trim()
  return firstLine.length > 160 ? `${firstLine.slice(0, 160)}…` : firstLine
}

function formatTime(value: string) {
  if (!value) return '时间未知'
  return new Intl.DateTimeFormat('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false }).format(new Date(value))
}
