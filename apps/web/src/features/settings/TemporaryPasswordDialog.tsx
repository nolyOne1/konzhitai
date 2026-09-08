import { KeyboardEvent, useEffect, useRef, useState } from 'react'

export function TemporaryPasswordDialog({ password: initialPassword, onClose, returnFocusTo }: { password: string; onClose(): void; returnFocusTo?: HTMLElement | null }) {
  const [password, setPassword] = useState(initialPassword)
  const [copyStatus, setCopyStatus] = useState('')
  const closeButton = useRef<HTMLButtonElement>(null)
  useEffect(() => { closeButton.current?.focus() }, [])

  async function copy() {
    try {
      await navigator.clipboard.writeText(password)
      setCopyStatus('已复制到剪贴板。')
    } catch {
      setCopyStatus('复制失败，请手动保存临时密码。')
    }
  }

  function close() {
    setPassword('')
    setCopyStatus('')
    onClose()
    queueMicrotask(() => returnFocusTo?.focus())
  }

  return (
    <div className="drawer-backdrop centered-dialog">
      <section className="console-dialog temporary-password-dialog" role="dialog" aria-modal="true" aria-labelledby="temporary-password-title" onKeyDown={(event) => trapDialog(event, close)}>
        <header className="drawer-header"><div><p className="eyebrow">仅显示一次</p><h2 id="temporary-password-title">一次性临时密码</h2><p>请通过安全渠道交给成员。关闭后无法再次查看，只能重新重置密码。</p></div><button className="icon-button" type="button" aria-label="关闭一次性临时密码窗口" onClick={close}>×</button></header>
        <div className="one-time-credential"><strong>临时密码</strong><code>{password}</code><small>成员登录后必须立即修改密码，才能进入控制台。</small></div>
        {copyStatus ? <p className="copy-status" role="status">{copyStatus}</p> : null}
        <footer className="dialog-actions"><button className="secondary-action" type="button" onClick={() => void copy()}>复制临时密码</button><button ref={closeButton} className="primary-action" type="button" onClick={close}>我已保存，关闭</button></footer>
      </section>
    </div>
  )
}

function trapDialog(event: KeyboardEvent<HTMLElement>, close: () => void) {
  if (event.key === 'Escape') { event.preventDefault(); close(); return }
  if (event.key !== 'Tab') return
  const controls = Array.from(event.currentTarget.querySelectorAll<HTMLElement>('button:not(:disabled), [tabindex="0"]'))
  const first = controls[0]
  const last = controls.at(-1)
  if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last?.focus() }
  else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first?.focus() }
}
