import { useEffect, useMemo, useRef, useState, type FormEvent, type KeyboardEvent } from 'react'

import { createAgentUpgradePlan, type AgentRelease, type AgentUpgradePlan, type ServerView } from '../../api/client'

interface AgentUpgradeDialogProps {
  releases: AgentRelease[]
  servers: ServerView[]
  onClose: () => void
  onCreated: (plan: AgentUpgradePlan) => void
}

export function AgentUpgradeDialog({ releases, servers, onClose, onCreated }: AgentUpgradeDialogProps) {
  const [step, setStep] = useState(1)
  const [releaseID, setReleaseID] = useState('')
  const [selectedIDs, setSelectedIDs] = useState<string[]>([])
  const [provider, setProvider] = useState('')
  const [region, setRegion] = useState('')
  const [label, setLabel] = useState('')
  const [version, setVersion] = useState('')
  const [batchSize, setBatchSize] = useState('1')
  const [drainTimeout, setDrainTimeout] = useState('3600')
  const [reconnectTimeout, setReconnectTimeout] = useState('120')
  const [verificationSeconds, setVerificationSeconds] = useState('30')
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [submitting, setSubmitting] = useState(false)
  const firstReleaseRef = useRef<HTMLInputElement>(null)
  const dialogRef = useRef<HTMLElement>(null)
  const targetRelease = releases.find((release) => release.id === releaseID)
  useEffect(() => firstReleaseRef.current?.focus(), [])

  const filteredServers = useMemo(() => servers.filter((server) => {
    if (provider && server.cloudProvider !== provider) return false
    if (region && !server.region.includes(region.trim())) return false
    if (version && !server.agentVersion.includes(version.trim())) return false
    if (label && !matchesLabel(server.labels, label)) return false
    return true
  }), [servers, provider, region, label, version])

  function eligible(server: ServerView) {
    if (!targetRelease || !server.enabled || !['online', 'draining'].includes(server.status)) return false
    if (!(server.agentCapabilities ?? []).includes('self_upgrade_v1') || server.agentVersion === targetRelease.version) return false
    return targetRelease.artifacts.some((artifact) => artifact.os === server.agentOS && artifact.arch === server.agentArch)
  }

  function next() {
    if (step === 1 && !targetRelease) { setErrors({ release: '请选择目标版本。' }); return }
    if (step === 2 && selectedIDs.filter((id) => servers.some((server) => server.id === id && eligible(server))).length === 0) { setErrors({ servers: '请至少选择一台支持控制台升级的服务器。' }); return }
    setErrors({}); setStep((value) => value + 1)
  }

  async function submit(event: FormEvent) {
    event.preventDefault()
    const validation = validateSettings(batchSize, drainTimeout, reconnectTimeout, verificationSeconds)
    if (Object.keys(validation).length) { setErrors(validation); return }
    if (!targetRelease) { setStep(1); setErrors({ release: '请选择目标版本。' }); return }
    const serverIDs = selectedIDs.filter((id) => servers.some((server) => server.id === id && eligible(server)))
    if (!serverIDs.length) { setStep(2); setErrors({ servers: '所选服务器已不再符合升级条件，请重新选择。' }); return }
    setSubmitting(true); setErrors({})
    try {
      const plan = await createAgentUpgradePlan({ targetReleaseId: targetRelease.id, serverIds: serverIDs, batchSize: Number(batchSize), drainTimeoutSeconds: Number(drainTimeout), reconnectTimeoutSeconds: Number(reconnectTimeout), verificationSeconds: Number(verificationSeconds) })
      onCreated(plan)
    } catch (reason) { setErrors({ submit: reason instanceof Error ? reason.message : '创建升级计划失败，请重试。' }) }
    finally { setSubmitting(false) }
  }

  return <div className="drawer-backdrop centered-dialog" onMouseDown={(event) => { if (event.target === event.currentTarget && !submitting) onClose() }}>
    <section ref={dialogRef} className="console-dialog upgrade-dialog" role="dialog" aria-modal="true" aria-labelledby="upgrade-dialog-title" onKeyDown={(event) => handleDialogKeys(event, () => { if (!submitting) onClose() }, dialogRef.current)}>
      <header className="drawer-header"><div><p className="eyebrow">代理版本管理</p><h2 id="upgrade-dialog-title">创建升级计划</h2><p>首批验证一台节点，确认健康后自动进入后续批次。</p></div><button type="button" className="icon-button" aria-label="关闭升级向导" disabled={submitting} onClick={onClose}><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M6 6l12 12M18 6L6 18" /></svg></button></header>
      <ol className="upgrade-wizard-steps" aria-label="升级计划步骤"><li className={step >= 1 ? 'is-active' : ''}><b>1</b><span>选择版本</span></li><li className={step >= 2 ? 'is-active' : ''}><b>2</b><span>选择服务器</span></li><li className={step >= 3 ? 'is-active' : ''}><b>3</b><span>确认策略</span></li></ol>
      {step === 1 ? <section className="upgrade-wizard-body" aria-labelledby="choose-release-title"><h3 id="choose-release-title">选择目标版本</h3><div className="release-choice-list">{releases.filter((release) => release.status === 'available').map((release, index) => <label key={release.id} className="release-choice"><input ref={index === 0 ? firstReleaseRef : undefined} type="radio" name="target-release" checked={releaseID === release.id} onChange={() => { setReleaseID(release.id); setSelectedIDs([]); setErrors({}) }} /><span><strong>{release.version}{release.recommended ? '（推荐）' : ''}</strong><small>{release.releaseNotes || '暂无发布说明'}</small></span></label>)}</div>{errors.release ? <p className="form-error" role="alert">{errors.release}</p> : null}</section> : null}
      {step === 2 ? <section className="upgrade-wizard-body" aria-labelledby="choose-server-title"><h3 id="choose-server-title">选择升级服务器</h3><div className="upgrade-filter-grid"><label className="form-field">云厂商筛选<select aria-label="云厂商筛选" value={provider} onChange={(event) => setProvider(event.target.value)}><option value="">全部云厂商</option>{[...new Set(servers.map((server) => server.cloudProvider).filter(Boolean))].map((item) => <option key={item}>{item}</option>)}</select></label><label className="form-field">地域筛选<input aria-label="地域筛选" value={region} onChange={(event) => setRegion(event.target.value)} /></label><label className="form-field">标签筛选<input aria-label="标签筛选" placeholder="用途=批处理" value={label} onChange={(event) => setLabel(event.target.value)} /></label><label className="form-field">当前版本筛选<input aria-label="当前版本筛选" placeholder="0.1.0" value={version} onChange={(event) => setVersion(event.target.value)} /></label></div><div className="upgrade-server-choices">{filteredServers.map((server) => <label key={server.id} className={`server-choice${eligible(server) ? '' : ' is-disabled'}`}><input type="checkbox" disabled={!eligible(server)} checked={selectedIDs.includes(server.id)} onChange={(event) => setSelectedIDs((ids) => event.target.checked ? [...ids, server.id] : ids.filter((id) => id !== server.id))} /><span><strong>{server.name}</strong><small>{server.cloudProvider} · {server.region} · {server.agentVersion || '版本未知'}</small></span><em>{eligible(server) ? '可升级' : eligibilityReason(server, targetRelease)}</em></label>)}</div>{errors.servers ? <p className="form-error" role="alert">{errors.servers}</p> : null}</section> : null}
      {step === 3 ? <form noValidate className="upgrade-wizard-body" onSubmit={(event) => void submit(event)}><h3>确认升级策略</h3><div className="canary-note"><strong>首批固定 1 台</strong><span>首台通过健康验证后，才会按下方批次规模继续。</span></div><div className="upgrade-settings-grid"><NumberField label="后续每批服务器数" value={batchSize} min={1} max={100} error={errors.batchSize} onChange={setBatchSize} /><NumberField label="排空超时（秒）" value={drainTimeout} min={60} max={86400} error={errors.drainTimeout} onChange={setDrainTimeout} /><NumberField label="重连超时（秒）" value={reconnectTimeout} min={30} max={3600} error={errors.reconnectTimeout} onChange={setReconnectTimeout} /><NumberField label="健康验证（秒）" value={verificationSeconds} min={10} max={600} error={errors.verificationSeconds} onChange={setVerificationSeconds} /></div><div className="upgrade-confirm-summary"><span>目标版本 <strong>{targetRelease?.version}</strong></span><span>升级节点 <strong>{selectedIDs.length} 台</strong></span></div>{errors.submit ? <p className="form-error" role="alert">{errors.submit}</p> : null}<footer className="dialog-actions"><button type="button" className="secondary-action" disabled={submitting} onClick={() => setStep(2)}>上一步</button><button type="submit" className="primary-action" disabled={submitting}>{submitting ? '正在创建…' : '启动升级计划'}</button></footer></form> : <footer className="dialog-actions upgrade-wizard-actions"><button type="button" className="secondary-action" onClick={step === 1 ? onClose : () => setStep(step - 1)}>{step === 1 ? '取消' : '上一步'}</button><button type="button" className="primary-action" onClick={next}>下一步</button></footer>}
    </section>
  </div>
}

function NumberField({ label, value, min, max, error, onChange }: { label: string; value: string; min: number; max: number; error?: string; onChange: (value: string) => void }) {
  return <label className="form-field">{label}<input aria-label={label} type="number" min={min} max={max} value={value} onChange={(event) => onChange(event.target.value)} />{error ? <small className="field-error" role="alert">{error}</small> : <small>允许范围：{min}–{max}</small>}</label>
}
function matchesLabel(labels: Record<string, string>, query: string) { const [key, ...rest] = query.split('='); return Boolean(key?.trim() && labels[key.trim()]?.includes(rest.join('=').trim())) }
function validateSettings(batch: string, drain: string, reconnect: string, verify: string) { const errors: Record<string, string> = {}; if (!between(batch, 1, 100)) errors.batchSize = '请输入 1 到 100。'; if (!between(drain, 60, 86400)) errors.drainTimeout = '请输入 60 到 86400。'; if (!between(reconnect, 30, 3600)) errors.reconnectTimeout = '请输入 30 到 3600。'; if (!between(verify, 10, 600)) errors.verificationSeconds = '请输入 10 到 600。'; return errors }
function between(value: string, min: number, max: number) { const number = Number(value); return Number.isInteger(number) && number >= min && number <= max }
function eligibilityReason(server: ServerView, release?: AgentRelease) { if (!(server.agentCapabilities ?? []).includes('self_upgrade_v1')) return '需人工升级基线'; if (!server.enabled || !['online', 'draining'].includes(server.status)) return '节点不可用'; if (release && server.agentVersion === release.version) return '已是目标版本'; return '无匹配安装包' }

function handleDialogKeys(event: KeyboardEvent<HTMLElement>, onClose: () => void, scope: HTMLElement | null) {
  if (event.key === 'Escape') { event.preventDefault(); onClose(); return }
  if (event.key !== 'Tab' || !scope) return
  const focusable = [...scope.querySelectorAll<HTMLElement>('button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])')]
  if (!focusable.length) return
  const first = focusable[0]
  const last = focusable[focusable.length - 1]
  if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus() }
  else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus() }
}
