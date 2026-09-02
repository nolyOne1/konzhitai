import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent, type KeyboardEvent, type MouseEvent, type RefObject } from 'react'

import { createServerEnrollmentToken, getLatestAgentRelease, type AgentReleaseManifest, type EnrollmentTokenView } from '../../api/client'
import { buildAgentInstallCommand } from './agentInstallCommand'

interface ServerEnrollmentDialogProps {
  controlUrl: string
  onClose: () => void
}

export function ServerEnrollmentDialog({ controlUrl, onClose }: ServerEnrollmentDialogProps) {
  const nameRef = useRef<HTMLInputElement>(null)
  const resultRef = useRef<HTMLHeadingElement>(null)
  const continueRef = useRef<HTMLButtonElement>(null)
  const mountedRef = useRef(true)
  const releaseRequestRef = useRef(0)
  const [name, setName] = useState('')
  const [cloudProvider, setCloudProvider] = useState('')
  const [region, setRegion] = useState('')
  const [labels, setLabels] = useState('')
  const [creating, setCreating] = useState(false)
  const [error, setError] = useState('')
  const [issued, setIssued] = useState<EnrollmentTokenView | null>(null)
  const [copyNotice, setCopyNotice] = useState<CopyNotice | null>(null)
  const [releaseState, setReleaseState] = useState<ReleaseState>('loading')
  const [release, setRelease] = useState<AgentReleaseManifest | null>(null)
  const [releaseError, setReleaseError] = useState('')
  const [confirmClose, setConfirmClose] = useState(false)

  const loadRelease = useCallback(async () => {
    const requestID = ++releaseRequestRef.current
    setReleaseState('loading')
    setRelease(null)
    setReleaseError('')
    try {
      const next = await getLatestAgentRelease()
      buildAgentInstallCommand(controlUrl, next)
      if (!mountedRef.current || releaseRequestRef.current !== requestID) return
      setRelease(next)
      setReleaseState('ready')
    } catch (reason) {
      if (!mountedRef.current || releaseRequestRef.current !== requestID) return
      setReleaseError(agentReleaseError(reason))
      setReleaseState('failed')
    }
  }, [controlUrl])

  useEffect(() => {
    mountedRef.current = true
    nameRef.current?.focus()
    return () => { mountedRef.current = false }
  }, [])

  useEffect(() => { void loadRelease() }, [loadRelease])

  useEffect(() => {
    if (confirmClose) continueRef.current?.focus()
    else if (issued) resultRef.current?.focus()
  }, [confirmClose, issued])

  const installCommand = useMemo(
    () => issued && releaseState === 'ready' && release ? buildAgentInstallCommand(controlUrl, release) : '',
    [controlUrl, issued, release, releaseState],
  )

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setError('')
    if (releaseState !== 'ready' || !release) {
      setError('代理版本尚未就绪，请重新加载后再试。')
      return
    }
    try {
      buildAgentInstallCommand(controlUrl, release)
    } catch (reason) {
      const message = agentReleaseError(reason)
      setRelease(null)
      setReleaseError(message)
      setReleaseState('failed')
      setError(message)
      return
    }
    let parsedLabels: Record<string, string>
    try {
      parsedLabels = parseLabels(labels)
    } catch (reason) {
      setError(chineseError(reason, LABEL_FORMAT_ERROR))
      return
    }
    setCreating(true)
    try {
      const token = await createServerEnrollmentToken({
        name: name.trim(),
        cloudProvider,
        region: region.trim(),
        labels: parsedLabels,
      })
      if (mountedRef.current) setIssued(token)
    } catch (reason) {
      if (mountedRef.current) setError(chineseError(reason, '创建服务器注册令牌失败，请检查网络后重试。'))
    } finally {
      if (mountedRef.current) setCreating(false)
    }
  }

  async function copy(value: string, success: string) {
    setCopyNotice(null)
    try {
      if (!navigator.clipboard?.writeText) throw new Error('当前浏览器不支持安全复制')
      await navigator.clipboard.writeText(value)
      if (mountedRef.current) setCopyNotice({ message: success, failed: false })
    } catch {
      if (mountedRef.current) setCopyNotice({ message: '复制失败，请手动选择内容。', failed: true })
    }
  }

  function requestClose() {
    if (creating) return
    if (issued) {
      setConfirmClose(true)
      return
    }
    onClose()
  }

  function closeFromBackdrop(event: MouseEvent<HTMLDivElement>) {
    if (event.target === event.currentTarget) requestClose()
  }

  return (
    <div className="drawer-backdrop centered-dialog" onMouseDown={closeFromBackdrop}>
      <section className="console-dialog enrollment-dialog" role="dialog" aria-modal="true" aria-labelledby="enrollment-title" onKeyDown={(event) => handleDialogKeys(event, confirmClose ? () => setConfirmClose(false) : requestClose)}>
        <header className="drawer-header">
          <div>
            <p className="eyebrow">多云节点扩容</p>
            <h2 id="enrollment-title">接入新服务器</h2>
            <p>{issued ? '令牌已创建，请在失效前完成代理注册。' : '填写节点信息并创建仅显示一次的注册令牌。'}</p>
          </div>
          <button type="button" className="icon-button" aria-label="关闭接入向导" disabled={creating} onClick={requestClose}>
            <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M6 6l12 12M18 6L6 18" /></svg>
          </button>
        </header>

        {confirmClose ? (
          <CloseConfirmation continueRef={continueRef} onContinue={() => setConfirmClose(false)} onConfirm={onClose} />
        ) : issued ? (
          <EnrollmentResult token={issued} installCommand={installCommand} copyNotice={copyNotice} resultRef={resultRef} onCopy={copy} onClose={requestClose} />
        ) : (
          <form className="enrollment-form" onSubmit={(event) => void submit(event)}>
            <div className="enrollment-steps" aria-label="接入进度"><strong>1</strong><span>填写节点信息</span><i aria-hidden="true" /><strong>2</strong><span>安装并连接代理</span></div>
            <AgentReleaseState state={releaseState} release={release} error={releaseError} onRetry={() => void loadRelease()} />
            {error ? <div className="form-error" role="alert">{error}</div> : null}
            <div className="enrollment-form-grid">
              <label className="form-field enrollment-span-2" htmlFor="enrollment-name">服务器名称<input id="enrollment-name" ref={nameRef} value={name} onChange={(event) => setName(event.target.value)} placeholder="例如：阿里云执行节点-1" required /></label>
              <label className="form-field" htmlFor="enrollment-provider">云厂商<select id="enrollment-provider" value={cloudProvider} onChange={(event) => setCloudProvider(event.target.value)} required><option value="">请选择云厂商</option><option value="腾讯云">腾讯云</option><option value="京东云">京东云</option><option value="阿里云">阿里云</option><option value="华为云">华为云</option><option value="自建服务器">自建服务器</option></select></label>
              <label className="form-field" htmlFor="enrollment-region">地域<input id="enrollment-region" value={region} onChange={(event) => setRegion(event.target.value)} placeholder="例如：华东 1" /></label>
              <label className="form-field enrollment-span-2" htmlFor="enrollment-labels">服务器标签<input id="enrollment-labels" value={labels} onChange={(event) => setLabels(event.target.value)} placeholder="用途=批处理, 环境=生产" /><small>使用半角逗号分隔多个“名称=值”标签，调度时要求全部匹配。</small></label>
            </div>
            <aside className="enrollment-security-note"><strong>安全边界</strong><p>注册令牌十分钟后自动失效且只能使用一次。云令不会保存服务器 SSH 用户名或密码。</p></aside>
            <footer className="dialog-actions"><button type="button" className="secondary-action" disabled={creating} onClick={requestClose}>取消</button><button type="submit" className="primary-action" disabled={creating || releaseState !== 'ready'}>{creating ? '正在创建…' : '创建一次性令牌'}</button></footer>
          </form>
        )}
      </section>
    </div>
  )
}

interface CopyNotice {
  message: string
  failed: boolean
}

type ReleaseState = 'loading' | 'ready' | 'failed'

function AgentReleaseState({ state, release, error, onRetry }: { state: ReleaseState; release: AgentReleaseManifest | null; error: string; onRetry: () => void }) {
  if (state === 'failed') {
    return <div className="enrollment-release-state is-failed" role="alert"><div><strong>代理版本读取失败</strong><span>{error}</span></div><button type="button" className="secondary-action" onClick={onRetry}>重新加载</button></div>
  }
  return <div className={`enrollment-release-state${state === 'ready' ? ' is-ready' : ''}`} aria-live="polite" aria-busy={state === 'loading'}>
    <div><strong>{state === 'loading' ? '正在读取代理版本' : `代理版本 ${release?.version}`}</strong><span>{state === 'loading' ? '正在校验可用安装包…' : '支持 Linux x86_64 / ARM64'}</span></div>
  </div>
}

function EnrollmentResult({ token, installCommand, copyNotice, resultRef, onCopy, onClose }: { token: EnrollmentTokenView; installCommand: string; copyNotice: CopyNotice | null; resultRef: RefObject<HTMLHeadingElement | null>; onCopy: (value: string, success: string) => Promise<void>; onClose: () => void }) {
  return <div className="enrollment-result">
    <div className="enrollment-success"><i aria-hidden="true" /><div><h3 ref={resultRef} role="status" aria-label="一条命令安装并接入" tabIndex={-1}>一条命令安装并接入</h3><strong>一次性令牌已创建</strong><p>有效期至 <time dateTime={token.expiresAt}>{formatTime(token.expiresAt)}</time>，关闭窗口后无法再次查看。</p></div></div>
    <section className="enrollment-secret" aria-labelledby="enrollment-token-title"><header><div><span>步骤 1</span><strong id="enrollment-token-title">保存注册令牌</strong></div><button type="button" className="secondary-action" onClick={() => void onCopy(token.token, '注册令牌已复制')}>复制注册令牌</button></header><code>{token.token}</code></section>
    <section className="enrollment-command" aria-labelledby="enrollment-command-title"><header><div><span>步骤 2</span><strong id="enrollment-command-title">在新服务器执行安装命令</strong></div><button type="button" className="secondary-action" onClick={() => void onCopy(installCommand, '安装命令已复制')}>复制安装命令</button></header><p>命令会自动识别服务器架构、下载安装包并校验完整性；安装器随后会在终端中隐藏读取上方令牌：</p><pre aria-label="代理安装命令"><code>{installCommand}</code></pre></section>
    {copyNotice ? <div className="credential-status enrollment-copy-status" role={copyNotice.failed ? 'alert' : 'status'}>{copyNotice.message}</div> : null}
    <aside className="enrollment-security-note is-warning"><strong>请勿泄露令牌</strong><p>不要通过聊天、工单或脚本参数传递。代理注册成功后令牌会立即作废，只保留节点独立凭据。</p></aside>
    <footer className="dialog-actions"><button type="button" className="primary-action" onClick={onClose}>完成并关闭</button></footer>
  </div>
}

function CloseConfirmation({ continueRef, onContinue, onConfirm }: { continueRef: RefObject<HTMLButtonElement | null>; onContinue: () => void; onConfirm: () => void }) {
  return <section className="enrollment-close-confirmation" role="alertdialog" aria-modal="true" aria-labelledby="enrollment-close-title" aria-describedby="enrollment-close-description">
    <strong id="enrollment-close-title">确认关闭接入向导</strong>
    <p id="enrollment-close-description">关闭后无法再次查看注册令牌。请先确认令牌和安装命令已经保存。</p>
    <div className="dialog-actions"><button ref={continueRef} type="button" className="secondary-action" onClick={onContinue}>继续查看</button><button type="button" className="primary-action" onClick={onConfirm}>确认关闭</button></div>
  </section>
}

function parseLabels(value: string) {
  const labels: Record<string, string> = {}
  const normalized = value.trim()
  if (!normalized) return labels
  if (normalized.includes('，')) throw new Error(LABEL_FORMAT_ERROR)

  for (const item of normalized.split(',')) {
    const separator = item.indexOf('=')
    if (separator <= 0 || separator === item.length - 1) throw new Error(LABEL_FORMAT_ERROR)
    const key = item.slice(0, separator).trim()
    const labelValue = item.slice(separator + 1).trim()
    if (!key || !labelValue || Object.hasOwn(labels, key)) throw new Error(LABEL_FORMAT_ERROR)
    labels[key] = labelValue
  }
  return labels
}

const LABEL_FORMAT_ERROR = '标签格式不正确，请使用半角逗号分隔“名称=值”，且名称和值都不能为空。'

function chineseError(reason: unknown, fallback: string) {
  const message = reason instanceof Error ? reason.message.trim() : ''
  return /[\u3400-\u9fff]/u.test(message) ? message : fallback
}

function agentReleaseError(reason: unknown) {
  if (reason instanceof Error && reason.message === '代理发布清单不完整，请重新加载。') return reason.message
  return '代理版本加载失败，请重试。'
}

function formatTime(value: string) {
  return new Intl.DateTimeFormat('zh-CN', { year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false }).format(new Date(value))
}

function handleDialogKeys(event: KeyboardEvent<HTMLElement>, onClose: () => void) {
  if (event.key === 'Escape') {
    event.preventDefault()
    onClose()
    return
  }
  if (event.key !== 'Tab') return
  const controls = [...event.currentTarget.querySelectorAll<HTMLElement>('button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), a[href]')]
  if (controls.length === 0) return
  const first = controls[0]
  const last = controls[controls.length - 1]
  if (event.shiftKey && document.activeElement === first) {
    event.preventDefault()
    last.focus()
  } else if (!event.shiftKey && document.activeElement === last) {
    event.preventDefault()
    first.focus()
  }
}
