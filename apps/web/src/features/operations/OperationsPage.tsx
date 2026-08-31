import { AccountSecurityPanel } from './AccountSecurityPanel'
import { NotificationSettingsPanel } from './NotificationSettingsPanel'

export function OperationsPage() {
  return (
    <>
      <div className="page-heading">
        <div>
          <p className="eyebrow">生产保障</p>
          <h1>运维中心</h1>
          <p>集中管理通知、备份、恢复校验与账号安全，所有操作保留审计记录。</p>
        </div>
      </div>

      <div className="operations-stack">
        <NotificationSettingsPanel />
        <AccountSecurityPanel />
      </div>
    </>
  )
}
