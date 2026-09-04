import { AccountSecurityPanel } from '../operations/AccountSecurityPanel'

interface RequiredPasswordPageProps {
  onChanged: () => void
}

export function RequiredPasswordPage({ onChanged }: RequiredPasswordPageProps) {
  return (
    <main className="required-password-page">
      <section className="required-password-card" aria-labelledby="required-password-title">
        <header className="required-password-heading">
          <a className="login-brand" href="/" aria-label="云令首页">
            <span className="brand-mark" aria-hidden="true">令</span>
            <span>
              <strong>云令</strong>
              <small>脚本调度中心</small>
            </span>
          </a>
          <div>
            <p className="eyebrow">账号安全</p>
            <h1 id="required-password-title">首次登录，请修改密码</h1>
            <p>临时密码只能用于首次登录，完成更新后才能进入控制台。</p>
          </div>
        </header>
        <AccountSecurityPanel required onChanged={onChanged} />
      </section>
    </main>
  )
}
