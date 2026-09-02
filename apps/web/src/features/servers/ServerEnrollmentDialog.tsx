import { useEffect, useMemo, useRef, useState, type FormEvent, type KeyboardEvent, type MouseEvent, type RefObject } from 'react'

import { createServerEnrollmentToken, type EnrollmentTokenView } from '../../api/client'

interface ServerEnrollmentDialogProps {
  controlUrl: string
  onClose: () => void
}

export function ServerEnrollmentDialog({ controlUrl, onClose }: ServerEnrollmentDialogProps) {
  const nameRef = useRef<HTMLInputElement>(null)
  const resultRef = useRef<HTMLDivElement>(null)
  const mountedRef = useRef(true)
  const [name, setName] = useState('')
  const [cloudProvider, setCloudProvider] = useState('')
  const [region, setRegion] = useState('')
  const [labels, setLabels] = useState('')
  const [creating, setCreating] = useState(false)
  const [error, setError] = useState('')
  const [issued, setIssued] = useState<EnrollmentTokenView | null>(null)
  const [copyNotice, setCopyNotice] = useState<CopyNotice | null>(null)

  useEffect(() => {
    mountedRef.current = true
    nameRef.current?.focus()
    return () => { mountedRef.current = false }
  }, [])

  useEffect(() => { if (issued) resultRef.current?.focus() }, [issued])

  const installCommand = useMemo(() => issued ? buildInstallCommand(controlUrl) : '', [controlUrl, issued])

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setError('')
    setCreating(true)
    try {
      const token = await createServerEnrollmentToken({
        name: name.trim(),
        cloudProvider,
        region: region.trim(),
        labels: parseLabels(labels),
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
    if (!creating) onClose()
  }

  function closeFromBackdrop(event: MouseEvent<HTMLDivElement>) {
    if (event.target === event.currentTarget) requestClose()
  }

  return (
    <div className="drawer-backdrop centered-dialog" onMouseDown={closeFromBackdrop}>
      <section className="console-dialog enrollment-dialog" role="dialog" aria-modal="true" aria-labelledby="enrollment-title" onKeyDown={(event) => handleDialogKeys(event, requestClose)}>
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

        {issued ? (
          <EnrollmentResult token={issued} installCommand={installCommand} copyNotice={copyNotice} resultRef={resultRef} onCopy={copy} onClose={requestClose} />
        ) : (
          <form className="enrollment-form" onSubmit={(event) => void submit(event)}>
            <div className="enrollment-steps" aria-label="接入进度"><strong>1</strong><span>填写节点信息</span><i aria-hidden="true" /><strong>2</strong><span>安装并连接代理</span></div>
            {error ? <div className="form-error" role="alert">{error}</div> : null}
            <div className="enrollment-form-grid">
              <label className="form-field enrollment-span-2" htmlFor="enrollment-name">服务器名称<input id="enrollment-name" ref={nameRef} value={name} onChange={(event) => setName(event.target.value)} placeholder="例如：阿里云执行节点-1" required /></label>
              <label className="form-field" htmlFor="enrollment-provider">云厂商<select id="enrollment-provider" value={cloudProvider} onChange={(event) => setCloudProvider(event.target.value)} required><option value="">请选择云厂商</option><option value="腾讯云">腾讯云</option><option value="京东云">京东云</option><option value="阿里云">阿里云</option><option value="华为云">华为云</option><option value="自建服务器">自建服务器</option></select></label>
              <label className="form-field" htmlFor="enrollment-region">地域<input id="enrollment-region" value={region} onChange={(event) => setRegion(event.target.value)} placeholder="例如：华东 1" /></label>
              <label className="form-field enrollment-span-2" htmlFor="enrollment-labels">服务器标签<input id="enrollment-labels" value={labels} onChange={(event) => setLabels(event.target.value)} placeholder="用途=批处理, 环境=生产" /><small>使用半角逗号分隔多个“名称=值”标签，调度时要求全部匹配。</small></label>
            </div>
            <aside className="enrollment-security-note"><strong>安全边界</strong><p>注册令牌十分钟后自动失效且只能使用一次。云令不会保存服务器 SSH 用户名或密码。</p></aside>
            <footer className="dialog-actions"><button type="button" className="secondary-action" disabled={creating} onClick={requestClose}>取消</button><button type="submit" className="primary-action" disabled={creating}>{creating ? '正在创建…' : '创建一次性令牌'}</button></footer>
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

function EnrollmentResult({ token, installCommand, copyNotice, resultRef, onCopy, onClose }: { token: EnrollmentTokenView; installCommand: string; copyNotice: CopyNotice | null; resultRef: RefObject<HTMLDivElement | null>; onCopy: (value: string, success: string) => Promise<void>; onClose: () => void }) {
  return <div className="enrollment-result">
    <div ref={resultRef} className="enrollment-success" role="status" tabIndex={-1}><i aria-hidden="true" /><div><strong>一次性令牌已创建</strong><p>有效期至 <time dateTime={token.expiresAt}>{formatTime(token.expiresAt)}</time>，关闭窗口后无法再次查看。</p></div></div>
    <section className="enrollment-secret" aria-labelledby="enrollment-token-title"><header><div><span>步骤 1</span><strong id="enrollment-token-title">保存注册令牌</strong></div><button type="button" className="secondary-action" onClick={() => void onCopy(token.token, '注册令牌已复制')}>复制注册令牌</button></header><code>{token.token}</code></section>
    <section className="enrollment-command" aria-labelledby="enrollment-command-title"><header><div><span>步骤 2</span><strong id="enrollment-command-title">在新服务器执行安装命令</strong></div><button type="button" className="secondary-action" onClick={() => void onCopy(installCommand, '安装命令已复制')}>复制安装命令</button></header><p>先将代理程序和安装文件上传到新服务器的 <code>/tmp</code>。命令会隐藏令牌输入，不把令牌值写入 Shell 历史：</p><pre aria-label="代理安装命令"><code>{installCommand}</code></pre></section>
    {copyNotice ? <div className="credential-status enrollment-copy-status" role={copyNotice.failed ? 'alert' : 'status'}>{copyNotice.message}</div> : null}
    <aside className="enrollment-security-note is-warning"><strong>请勿泄露令牌</strong><p>不要通过聊天、工单或脚本参数传递。代理注册成功后令牌会立即作废，只保留节点独立凭据。</p></aside>
    <footer className="dialog-actions"><button type="button" className="primary-action" onClick={onClose}>完成并关闭</button></footer>
  </div>
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

function shellQuote(value: string) {
  return `'${value.replaceAll("'", `'"'"'`)}'`
}

function buildInstallCommand(controlUrl: string) {
  return [
    `YUNLING_CONTROL_URL=${shellQuote(controlUrl)} bash -s <<'YUNLING_INSTALL'`,
    'set -euo pipefail',
    "trap 'unset YUNLING_ENROLLMENT_TOKEN' EXIT",
    "read -rsp '请输入一次性注册令牌：' YUNLING_ENROLLMENT_TOKEN </dev/tty",
    "printf '\\n'",
    `printf '%s\\n' "$YUNLING_ENROLLMENT_TOKEN" | sudo env YUNLING_CONTROL_URL="$YUNLING_CONTROL_URL" bash -c 'IFS= read -r YUNLING_ENROLLMENT_TOKEN; export YUNLING_ENROLLMENT_TOKEN; exec bash /tmp/install.sh /tmp/yunling-agent'`,
    'YUNLING_INSTALL',
  ].join('\n')
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
