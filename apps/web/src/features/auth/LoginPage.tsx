import { FormEvent, useState } from 'react'

import '../../app/styles.css'

type ErrorResponse = {
  message?: string
}

export function LoginPage() {
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [submitting, setSubmitting] = useState(false)

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setError('')
    setSubmitting(true)

    try {
      const response = await fetch('/api/auth/login', {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ email: email.trim(), password }),
      })
      if (!response.ok) {
        const body = await response.json() as ErrorResponse
        setError(body.message || '登录失败，请稍后重试')
        return
      }
      window.location.assign('/')
    } catch {
      setError('无法连接登录服务，请检查网络后重试')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <main className="login-page">
      <section className="login-intro" aria-labelledby="login-brand">
        <a className="login-brand" href="/" aria-label="云令首页">
          <span className="brand-mark" aria-hidden="true">令</span>
          <span>
            <strong id="login-brand">云令</strong>
            <small>脚本调度中心</small>
          </span>
        </a>
        <div className="login-intro-copy">
          <p className="eyebrow">多服务器任务管理</p>
          <h1>让每一个脚本，都在合适的服务器上运行</h1>
          <p>集中管理脚本版本、定时任务、资源调度、运行状态与日志。</p>
        </div>
        <p className="login-security">代理主动连接 · 服务端会话 · 操作全程审计</p>
      </section>

      <section className="login-panel" aria-labelledby="login-title">
        <form className="login-card" onSubmit={submit}>
          <header>
            <p className="eyebrow">管理控制台</p>
            <h2 id="login-title">登录云令</h2>
            <p>使用团队分配的账号进入脚本调度中心。</p>
          </header>

          <label className="form-field">
            <span>邮箱</span>
            <input
              autoComplete="username"
              inputMode="email"
              name="email"
              onChange={(event) => setEmail(event.target.value)}
              placeholder="请输入邮箱地址"
              required
              type="email"
              value={email}
            />
          </label>

          <label className="form-field">
            <span>密码</span>
            <input
              autoComplete="current-password"
              name="password"
              onChange={(event) => setPassword(event.target.value)}
              placeholder="请输入密码"
              required
              type="password"
              value={password}
            />
          </label>

          {error ? <p className="form-error" role="alert">{error}</p> : null}

          <button className="login-submit" disabled={submitting} type="submit">
            {submitting ? '正在登录…' : '登录'}
          </button>
          <p className="login-help">无法登录？请联系系统管理员检查账号状态。</p>
        </form>
      </section>
    </main>
  )
}
