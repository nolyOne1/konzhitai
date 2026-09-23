import type { ScriptSyncView } from '../../api/client'

export function syncRetryDescription(item: ScriptSyncView) {
  if (!item.failureCount) return ''
  const count = `连续失败 ${item.failureCount} 次`
  if (item.nextRetryAt) {
    const time = new Intl.DateTimeFormat('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit' }).format(new Date(item.nextRetryAt))
    return `${count}，将在 ${time} 自动重试`
  }
  if (item.state === 'failed' && item.failureCount >= (item.retryLimit || 3)) return `${count}，已达到重试上限，请排查后手动重试；此版本不可调度`
  return `${count}，正在重新同步`
}
