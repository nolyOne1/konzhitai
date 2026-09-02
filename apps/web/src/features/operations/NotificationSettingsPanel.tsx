import { FormEvent, useEffect, useRef, useState } from 'react'

import {
  getFeishuNotificationConfig,
  getNotificationDelivery,
  getSession,
  testFeishuNotification,
  updateFeishuNotificationConfig,
  type FeishuNotificationConfig,
  type NotificationDelivery,
} from '../../api/client'

interface NotificationSettingsPanelProps {
  pollIntervalMs?: number
}

export function NotificationSettingsPanel({ pollIntervalMs = 1000 }: NotificationSettingsPanelProps) {
  const [config, setConfig] = useState<FeishuNotificationConfig>({ configured: false, enabled: false, maskedDestination: '' })
  const [isAdmin, setIsAdmin] = useState(false)
  const [loading, setLoading] = useState(true)
  const [enabled, setEnabled] = useState(false)
  const [webhook, setWebhook] = useState('')
  const [signingSecret, setSigningSecret] = useState('')
  const [saving, setSaving] = useState(false)
  const [testing, setTesting] = useState(false)
  const [error, setError] = useState('')
  const [status, setStatus] = useState('')
  const activeRef = useRef(true)
  const errorRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    activeRef.current = true
    Promise.all([getFeishuNotificationConfig(), getSession()])
      .then(([loadedConfig, session]) => {
        if (!activeRef.current) return
        setConfig(loadedConfig)
        setEnabled(loadedConfig.enabled)
        setIsAdmin(session.roles.includes('admin'))
      })
      .catch((reason: unknown) => {
        if (activeRef.current) setError(reason instanceof Error ? reason.message : '读取飞书通知配置失败')
      })
      .finally(() => { if (activeRef.current) setLoading(false) })
    return () => { activeRef.current = false }
  }, [])

  useEffect(() => { if (error) errorRef.current?.focus() }, [error])

  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setError('')
    setStatus('')
    const hasWebhook = webhook.length > 0
    const hasSigningSecret = signingSecret.length > 0
    if (hasWebhook !== hasSigningSecret || (!config.configured && !hasWebhook)) {
      setError('首次配置或轮换时必须同时填写飞书 Webhook 和签名密钥。')
      return
    }
    setSaving(true)
    try {
      const updated = await updateFeishuNotificationConfig({ enabled, webhook, signingSecret })
      if (!activeRef.current) return
      setConfig(updated)
      setEnabled(updated.enabled)
      setWebhook('')
      setSigningSecret('')
      setStatus('飞书通知设置已保存')
    } catch (reason) {
      if (activeRef.current) setError(reason instanceof Error ? reason.message : '保存飞书通知设置失败')
    } finally {
      if (activeRef.current) setSaving(false)
    }
  }

  async function sendTest() {
    setError('')
    setStatus('正在发送飞书测试消息…')
    setTesting(true)
    try {
      let delivery = await testFeishuNotification()
      const deadline = Date.now() + 30_000
      while (activeRef.current && !finished(delivery) && Date.now() < deadline) {
        await delay(pollIntervalMs)
        if (!activeRef.current) return
        delivery = await getNotificationDelivery(delivery.id)
      }
      if (!activeRef.current) return
      if (delivery.status === 'sent') {
        setStatus('飞书测试消息已发送')
      } else if (delivery.status === 'failed') {
        setStatus('')
        setError(boundedError(delivery.lastError))
      } else {
        setStatus('')
        setError('测试消息仍在发送队列中，请稍后重试。')
      }
    } catch (reason) {
      if (activeRef.current) {
        setStatus('')
        setError(reason instanceof Error ? reason.message : '发送飞书测试消息失败')
      }
    } finally {
      if (activeRef.current) setTesting(false)
    }
  }

  return (
    <section className="panel settings-panel notification-settings-panel" aria-labelledby="feishu-notification-title">
      <header className="panel-header">
        <div>
          <h2 id="feishu-notification-title">飞书通知</h2>
          <p>告警开启与恢复消息由独立运维服务可靠投递，敏感配置不会回显。</p>
        </div>
        <span className={`notification-state ${config.enabled ? 'is-enabled' : ''}`}>
          {loading ? '读取中' : config.enabled ? '已启用' : config.configured ? '已停用' : '未配置'}
        </span>
      </header>

      <div className="notification-settings-body">
        {error ? <div ref={errorRef} className="form-error error-summary" role="alert" tabIndex={-1}>{error}</div> : null}
        {status ? <div className="notice notice-success notification-status" role="status">{status}</div> : null}

        <div className="notification-overview" aria-label="飞书通知配置状态">
          <div>
            <span>通知目标</span>
            <strong>{config.configured ? config.maskedDestination : '尚未配置'}</strong>
            <small>控制台始终只显示脱敏后的机器人标识。</small>
          </div>
          <div>
            <span>投递策略</span>
            <strong>持久化发件箱</strong>
            <small>失败按 1 分钟至 6 小时分级退避，最多尝试 24 次。</small>
          </div>
          <div>
            <span>告警阈值</span>
            <strong>两级恢复线</strong>
            <small>离线、排队、失败及服务器资源告警均自动恢复。</small>
          </div>
        </div>

        {loading ? <div className="compact-empty" aria-live="polite"><span aria-hidden="true" />正在读取飞书通知配置…</div> : isAdmin ? (
          <form className="notification-form" onSubmit={save} noValidate>
            <label className="notification-toggle" htmlFor="feishu-enabled">
              <input id="feishu-enabled" type="checkbox" aria-label="启用飞书通知" checked={enabled} onChange={(event) => setEnabled(event.target.checked)} />
              <span><strong>启用飞书通知</strong><small>停用时保留加密配置，之后可直接重新启用。</small></span>
            </label>
            <div className="notification-secret-grid">
              <div className="form-field">
                <label htmlFor="feishu-webhook">飞书 Webhook</label>
                <input id="feishu-webhook" type="url" value={webhook} onChange={(event) => setWebhook(event.target.value)} autoComplete="off" placeholder={config.configured ? '留空表示沿用当前配置' : 'https://open.feishu.cn/open-apis/bot/v2/hook/…'} />
                <small>只接受 open.feishu.cn 的 V2 自定义机器人地址。</small>
              </div>
              <div className="form-field">
                <label htmlFor="feishu-signing-secret">签名密钥</label>
                <input id="feishu-signing-secret" type="password" value={signingSecret} onChange={(event) => setSigningSecret(event.target.value)} autoComplete="new-password" placeholder={config.configured ? '留空表示沿用当前配置' : '输入机器人签名密钥'} />
                <small>保存后立即清空，任何接口都不会再次返回。</small>
              </div>
            </div>
            <div className="notification-actions">
              <button className="secondary-action" type="button" onClick={sendTest} disabled={!config.configured || !config.enabled || testing || saving}>
                {testing ? '正在测试…' : '发送测试消息'}
              </button>
              <button className="primary-action" type="submit" disabled={saving || testing}>
                {saving ? '正在保存…' : '保存通知设置'}
              </button>
            </div>
          </form>
        ) : (
          <div className="notification-readonly-note">
            <strong>当前账号仅可查看通知状态。</strong>
            <p>如需录入、轮换、启停或测试飞书机器人，请联系管理员。</p>
          </div>
        )}
      </div>
    </section>
  )
}

function finished(delivery: NotificationDelivery) {
  return delivery.status === 'sent' || delivery.status === 'failed'
}

function boundedError(value?: string) {
  const message = value?.trim() || '飞书测试消息发送失败'
  return Array.from(message).slice(0, 256).join('')
}

function delay(milliseconds: number) {
  return new Promise<void>((resolve) => window.setTimeout(resolve, milliseconds))
}
