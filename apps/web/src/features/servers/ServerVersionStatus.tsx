import type { ServerView } from '../../api/client'

interface ServerVersionStatusProps {
  server: ServerView
  recommendedVersion?: string
}

const activeUpgradeStatuses = new Set(['waiting', 'draining', 'downloading', 'verifying', 'installing', 'reconnecting', 'health_checking', 'rolling_back'])

export function ServerVersionStatus({ server, recommendedVersion }: ServerVersionStatusProps) {
  const { label, tone } = versionStatus(server, recommendedVersion)
  return <span className={`version-status version-status-${tone}`}><i aria-hidden="true" />{label}</span>
}

function versionStatus(server: ServerView, recommendedVersion?: string) {
  if (server.upgradeStatus === 'manual_intervention') return { label: '升级失败', tone: 'danger' }
  if (server.upgradeStatus && activeUpgradeStatuses.has(server.upgradeStatus)) return { label: '升级中', tone: 'pending' }
  if (!server.agentVersion) return { label: '版本未知', tone: 'muted' }
  if (!(server.agentCapabilities ?? []).includes('self_upgrade_v1')) return { label: '需人工升级基线', tone: 'warning' }
  if (!recommendedVersion) return { label: '版本未知', tone: 'muted' }
  if (server.agentVersion === recommendedVersion) return { label: '已是最新版', tone: 'success' }
  return { label: '可升级', tone: 'info' }
}
