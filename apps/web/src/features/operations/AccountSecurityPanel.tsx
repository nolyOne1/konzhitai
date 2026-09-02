import { FormEvent, useEffect, useRef, useState } from 'react'

import { changePassword } from '../../api/client'

type PasswordField = 'current-password' | 'new-password' | 'confirm-password'

interface PasswordFormError {
  message: string
  field?: PasswordField
}

export function AccountSecurityPanel() {
  const [currentPassword, setCurrentPassword] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [confirmation, setConfirmation] = useState('')
  const [error, setError] = useState<PasswordFormError | null>(null)
  const [status, setStatus] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const errorRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (error) errorRef.current?.focus()
  }, [error])

  function updateField(setter: (value: string) => void, value: string) {
    setter(value)
    setError(null)
    setStatus('')
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setStatus('')
    if (!currentPassword) {
      setError({ field: 'current-password', message: '请输入当前密码' })
      return
    }
    if (Array.from(newPassword).length < 12) {
      setError({ field: 'new-password', message: '新密码至少需要 12 位' })
      return
    }
    if (newPassword === currentPassword) {
      setError({ field: 'new-password', message: '新密码不能与当前密码相同' })
      return
    }
    if (confirmation !== newPassword) {
      setError({ field: 'confirm-password', message: '两次输入的新密码不一致' })
      return
    }

    setError(null)
    setSubmitting(true)
    try {
      await changePassword(currentPassword, newPassword)
      setCurrentPassword('')
      setNewPassword('')
      setConfirmation('')
      setStatus('密码已更新，其他设备已退出')
    } catch (reason) {
      setError({ message: reason instanceof Error ? reason.message : '密码更新失败，请稍后重试' })
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <section className="panel settings-panel account-security-panel" aria-labelledby="account-security-title">
      <header className="panel-header">
        <div>
          <h2 id="account-security-title">账号安全</h2>
          <p>修改密码后，当前会话继续保留，其他设备需要重新登录。</p>
        </div>
        <span>至少 12 位</span>
      </header>

      <div className="account-security-body">
        <div className="session-safety-note" aria-label="会话安全说明">
          <span className="session-safety-mark" aria-hidden="true" />
          <div>
            <strong>本次登录不会中断</strong>
            <p>提交成功后，其他浏览器和设备上的会话会立即失效。</p>
          </div>
        </div>

        <form className="password-form" onSubmit={submit} noValidate>
          {error ? (
            <div ref={errorRef} className="form-error error-summary" role="alert" tabIndex={-1}>
              {error.field ? <a href={`#${error.field}`}>{error.message}</a> : error.message}
            </div>
          ) : null}
          {status ? <div className="notice notice-success password-status" role="status">{status}</div> : null}

          <div className="password-form-grid">
            <div className="form-field">
              <label htmlFor="current-password">当前密码</label>
              <input
                id="current-password"
                type="password"
                autoComplete="current-password"
                value={currentPassword}
                onChange={(event) => updateField(setCurrentPassword, event.target.value)}
                aria-invalid={error?.field === 'current-password'}
                aria-describedby="current-password-help"
              />
              <small id="current-password-help">用于确认当前账号身份。</small>
            </div>

            <div className="form-field">
              <label htmlFor="new-password">新密码</label>
              <input
                id="new-password"
                type="password"
                autoComplete="new-password"
                value={newPassword}
                onChange={(event) => updateField(setNewPassword, event.target.value)}
                aria-invalid={error?.field === 'new-password'}
                aria-describedby="new-password-help"
              />
              <small id="new-password-help">至少 12 位，且不能与当前密码相同。</small>
            </div>

            <div className="form-field">
              <label htmlFor="confirm-password">确认新密码</label>
              <input
                id="confirm-password"
                type="password"
                autoComplete="new-password"
                value={confirmation}
                onChange={(event) => updateField(setConfirmation, event.target.value)}
                aria-invalid={error?.field === 'confirm-password'}
                aria-describedby="confirm-password-help"
              />
              <small id="confirm-password-help">再次输入新密码，避免录入错误。</small>
            </div>
          </div>

          <div className="password-form-actions">
            <button className="primary-action" type="submit" disabled={submitting}>
              {submitting ? '正在更新…' : '更新密码'}
            </button>
          </div>
        </form>
      </div>
    </section>
  )
}
