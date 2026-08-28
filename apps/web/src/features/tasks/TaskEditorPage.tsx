import { useEffect, useId, useMemo, useRef, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'

import {
  createTask,
  createTaskSchedule,
  getScript,
  getScripts,
  getTask,
  updateTask,
  validateTaskCron,
  type ScriptVersion,
  type ScriptView,
  type TaskInput,
  type TaskVersionPolicy,
} from '../../api/client'

type EditorState = {
  name: string
  description: string
  scriptId: string
  versionPolicy: TaskVersionPolicy
  pinnedVersionId: string
  requiredRuntime: string
  parameters: string
  secretRefs: string
  requiredLabels: string
  cpuMillicores: number
  memoryMB: number
  diskMB: number
  priority: number
  maxConcurrency: number
  timeoutSeconds: number
  maxWaitSeconds: number
  maxRetries: number
  backoffSeconds: number
  idempotent: boolean
  enabled: boolean
  scheduleEnabled: boolean
  cronExpression: string
  timezone: string
}

const emptyEditor: EditorState = {
  name: '', description: '', scriptId: '', versionPolicy: 'latest', pinnedVersionId: '', requiredRuntime: 'bash',
  parameters: '{}', secretRefs: '', requiredLabels: '', cpuMillicores: 100, memoryMB: 128, diskMB: 128,
  priority: 50, maxConcurrency: 1, timeoutSeconds: 3600, maxWaitSeconds: 86400,
  maxRetries: 0, backoffSeconds: 30, idempotent: false, enabled: true,
  scheduleEnabled: false, cronExpression: '0 2 * * *', timezone: 'Asia/Shanghai',
}

export function TaskEditorPage() {
  const { id = '' } = useParams()
  const editing = Boolean(id)
  const navigate = useNavigate()
  const [scripts, setScripts] = useState<ScriptView[]>([])
  const [versions, setVersions] = useState<ScriptVersion[]>([])
  const [editor, setEditor] = useState<EditorState>(emptyEditor)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const [parameterError, setParameterError] = useState('')
  const errorRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    let active = true
    Promise.all([getScripts(), editing ? getTask(id) : Promise.resolve(null)])
      .then(([scriptList, definition]) => {
        if (!active) return
        setScripts(scriptList)
        if (definition) setEditor(editorFromDefinition(definition))
      })
      .catch((reason: unknown) => { if (active) setError(reason instanceof Error ? reason.message : '任务编辑器加载失败') })
      .finally(() => { if (active) setLoading(false) })
    return () => { active = false }
  }, [editing, id])

  useEffect(() => {
    if (!editor.scriptId || editor.versionPolicy !== 'pinned') return
    let active = true
    getScript(editor.scriptId)
      .then((detail) => { if (active) setVersions(detail.versions) })
      .catch((reason: unknown) => { if (active) setError(reason instanceof Error ? reason.message : '读取脚本版本失败') })
    return () => { active = false }
  }, [editor.scriptId, editor.versionPolicy])

  const selectedScript = useMemo(() => scripts.find((script) => script.id === editor.scriptId), [editor.scriptId, scripts])

  function selectScript(scriptId: string) {
    const script = scripts.find((item) => item.id === scriptId)
    setEditor((current) => ({
      ...current,
      scriptId,
      requiredRuntime: script?.runtime || current.requiredRuntime,
      pinnedVersionId: current.versionPolicy === 'pinned' ? script?.currentVersionId || '' : '',
    }))
  }

  async function submit(event: React.FormEvent) {
    event.preventDefault()
    setError('')
    setParameterError('')
    if (!editor.name.trim() || !editor.scriptId) {
      setError('请填写任务名称并选择执行脚本。')
      queueMicrotask(() => errorRef.current?.focus())
      return
    }
    let parameters: Record<string, unknown>
    try {
      parameters = parseJSONObject(editor.parameters)
    } catch {
      setError('任务参数必须是 JSON 对象，请修正后重试。')
      setParameterError('任务参数必须是 JSON 对象。')
      queueMicrotask(() => errorRef.current?.focus())
      return
    }
    const input: TaskInput = {
      name: editor.name.trim(), description: editor.description.trim(), scriptId: editor.scriptId,
      versionPolicy: editor.versionPolicy, pinnedVersionId: editor.versionPolicy === 'pinned' ? editor.pinnedVersionId : undefined,
      parameters, secretRefs: parsePairs(editor.secretRefs), priority: editor.priority,
      requiredLabels: parsePairs(editor.requiredLabels), requiredRuntime: editor.requiredRuntime,
      resources: { cpuMillicores: editor.cpuMillicores, memoryBytes: editor.memoryMB * 1048576, diskBytes: editor.diskMB * 1048576 },
      maxConcurrency: editor.maxConcurrency, timeoutSeconds: editor.timeoutSeconds, maxWaitSeconds: editor.maxWaitSeconds,
      retryPolicy: { maxRetries: editor.maxRetries, backoffSeconds: editor.backoffSeconds },
      idempotent: editor.idempotent, enabled: editor.enabled,
    }
    setSaving(true)
    try {
      if (editor.scheduleEnabled) {
        await validateTaskCron({ cronExpression: editor.cronExpression, timezone: editor.timezone })
      }
      const saved = editing ? await updateTask(id, input) : await createTask(input)
      if (!editing && editor.scheduleEnabled) {
        await createTaskSchedule(saved.id, { cronExpression: editor.cronExpression, timezone: editor.timezone, enabled: true })
      }
      navigate('/tasks', { state: { message: editing ? '任务已更新' : '任务已创建' } })
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '保存任务失败')
      queueMicrotask(() => errorRef.current?.focus())
    } finally {
      setSaving(false)
    }
  }

  if (loading) return <div className="page-loading" aria-live="polite">正在加载任务编辑器…</div>

  return (
    <>
      <div className="editor-page-heading task-editor-heading">
        <div><Link className="back-link" to="/tasks">← 返回任务调度</Link><p className="eyebrow">运行规则配置</p><h1>{editing ? `编辑${editor.name}` : '新建任务'}</h1><p>选择中央脚本并定义资源、优先级和排队规则；没有合适服务器时会继续等待。</p></div>
      </div>

      {error && <div ref={errorRef} className="notice notice-error" role="alert" tabIndex={-1}>{error}</div>}

      <form id="task-editor-form" className="task-editor-form" onSubmit={(event) => void submit(event)}>
        <section className="panel task-form-section" aria-labelledby="task-basic-title">
          <header className="panel-header"><div><h2 id="task-basic-title">基础信息</h2><p>定义团队可识别的任务名称和唯一执行脚本。</p></div></header>
          <div className="task-form-grid">
            <label className="form-field task-span-2">任务名称<input value={editor.name} onChange={(event) => setEditor({ ...editor, name: event.target.value })} placeholder="例如：每日归档任务" /></label>
            <label className="form-field task-span-2">任务说明<textarea value={editor.description} onChange={(event) => setEditor({ ...editor, description: event.target.value })} placeholder="说明任务目的、负责人和使用边界" /></label>
            <label className="form-field">执行脚本<select value={editor.scriptId} onChange={(event) => selectScript(event.target.value)}><option value="">请选择已发布脚本</option>{scripts.filter((script) => script.currentVersion > 0).map((script) => <option key={script.id} value={script.id}>{script.name} · 版本 {script.currentVersion}</option>)}</select></label>
            <label className="form-field">版本策略<select value={editor.versionPolicy} onChange={(event) => setEditor({ ...editor, versionPolicy: event.target.value as TaskVersionPolicy, pinnedVersionId: event.target.value === 'pinned' ? selectedScript?.currentVersionId || '' : '' })}><option value="latest">执行时锁定最新版本</option><option value="pinned">固定指定版本</option></select></label>
            {editor.versionPolicy === 'pinned' && <label className="form-field task-span-2">固定版本<select value={editor.pinnedVersionId} onChange={(event) => setEditor({ ...editor, pinnedVersionId: event.target.value })}><option value="">请选择版本</option>{versions.map((version) => <option key={version.id} value={version.id}>版本 {version.number} · {version.releaseNotes}</option>)}</select></label>}
          </div>
        </section>

        <section className="panel task-form-section" aria-labelledby="task-rules-title">
          <header className="panel-header"><div><h2 id="task-rules-title">参数与分配规则</h2><p>参数在创建运行实例时形成快照；标签用于筛选可分配服务器。</p></div></header>
          <div className="task-form-grid">
            <div className="form-field task-span-2"><label htmlFor="task-parameters">任务参数（JSON）</label><textarea id="task-parameters" aria-invalid={Boolean(parameterError)} aria-describedby={parameterError ? 'task-parameters-error' : 'task-parameters-help'} value={editor.parameters} onChange={(event) => setEditor({ ...editor, parameters: event.target.value })} /><small id="task-parameters-help">只填写普通参数；敏感值请通过密钥引用绑定。</small>{parameterError && <small id="task-parameters-error" className="field-error">{parameterError}</small>}</div>
            <div className="form-field"><label htmlFor="task-labels">服务器标签</label><textarea id="task-labels" className="compact-textarea" aria-describedby="task-labels-help" value={editor.requiredLabels} onChange={(event) => setEditor({ ...editor, requiredLabels: event.target.value })} placeholder={'用途=批处理\n环境=生产'} /><small id="task-labels-help">每行一个“键=值”，全部匹配才可分配。</small></div>
            <div className="form-field"><label htmlFor="task-secrets">密钥引用</label><textarea id="task-secrets" className="compact-textarea" aria-describedby="task-secrets-help" value={editor.secretRefs} onChange={(event) => setEditor({ ...editor, secretRefs: event.target.value })} placeholder="访问令牌=archive-token" /><small id="task-secrets-help">这里只保存密钥名称，不保存明文。</small></div>
          </div>
        </section>

        <section className="panel task-form-section" aria-labelledby="task-resource-title">
          <header className="panel-header"><div><h2 id="task-resource-title">资源与排队规则</h2><p>资源不足时任务保持排队；服务器空闲或资源恢复后自动尝试分配。</p></div></header>
          <div className="task-number-grid">
            <NumberField label="CPU（毫核）" min={1} value={editor.cpuMillicores} onChange={(value) => setEditor({ ...editor, cpuMillicores: value })} />
            <NumberField label="内存（MB）" min={1} value={editor.memoryMB} onChange={(value) => setEditor({ ...editor, memoryMB: value })} />
            <NumberField label="磁盘（MB）" min={1} value={editor.diskMB} onChange={(value) => setEditor({ ...editor, diskMB: value })} />
            <NumberField label="优先级（0–100）" min={0} max={100} value={editor.priority} onChange={(value) => setEditor({ ...editor, priority: value })} />
            <NumberField label="最大并发" min={1} value={editor.maxConcurrency} onChange={(value) => setEditor({ ...editor, maxConcurrency: value })} />
            <NumberField label="执行超时（秒）" min={1} value={editor.timeoutSeconds} onChange={(value) => setEditor({ ...editor, timeoutSeconds: value })} />
            <NumberField label="最大等待（秒）" min={1} value={editor.maxWaitSeconds} onChange={(value) => setEditor({ ...editor, maxWaitSeconds: value })} />
            <NumberField label="失败重试次数" min={0} value={editor.maxRetries} onChange={(value) => setEditor({ ...editor, maxRetries: value })} />
            <NumberField label="重试间隔（秒）" min={0} value={editor.backoffSeconds} onChange={(value) => setEditor({ ...editor, backoffSeconds: value })} />
          </div>
          <div className="task-switches"><CheckField label="幂等任务" hint="声明后，调度器可在明确失败时安全重试。" checked={editor.idempotent} onChange={(value) => setEditor({ ...editor, idempotent: value })} /><CheckField label="创建后立即启用" hint="停用后不会产生新的手动或定时运行实例。" checked={editor.enabled} onChange={(value) => setEditor({ ...editor, enabled: value })} /></div>
        </section>

        <section className="panel task-form-section" aria-labelledby="task-schedule-title">
          <header className="panel-header"><div><h2 id="task-schedule-title">定时计划</h2><p>采用五段 Cron 表达式和 IANA 时区；默认按中国标准时间执行。</p></div></header>
          <CheckField label="启用定时执行" hint="关闭时仍可在任务列表中手动执行。" checked={editor.scheduleEnabled} onChange={(value) => setEditor({ ...editor, scheduleEnabled: value })} />
          {editor.scheduleEnabled && <div className="task-form-grid schedule-fields"><label className="form-field">Cron 表达式<input value={editor.cronExpression} onChange={(event) => setEditor({ ...editor, cronExpression: event.target.value })} placeholder="0 2 * * *" /><small>示例：每天凌晨 2 点为 0 2 * * *</small></label><label className="form-field">时区<select value={editor.timezone} onChange={(event) => setEditor({ ...editor, timezone: event.target.value })}><option value="Asia/Shanghai">Asia/Shanghai（中国标准时间）</option><option value="UTC">UTC（协调世界时）</option></select></label></div>}
        </section>

        <footer className="task-form-actions"><Link className="secondary-action button-link" to="/tasks">取消</Link><button type="submit" className="primary-action" disabled={saving}>{saving ? '正在保存…' : editing ? '保存任务' : '创建任务'}</button></footer>
      </form>
    </>
  )
}

function NumberField({ label, value, min, max, onChange }: { label: string; value: number; min: number; max?: number; onChange: (value: number) => void }) {
  return <label className="form-field">{label}<input type="number" min={min} max={max} inputMode="numeric" value={value} onChange={(event) => onChange(Number(event.target.value))} /></label>
}

function CheckField({ label, hint, checked, onChange }: { label: string; hint: string; checked: boolean; onChange: (value: boolean) => void }) {
  const id = useId()
  return <div className="check-card"><input id={id} type="checkbox" aria-describedby={`${id}-hint`} checked={checked} onChange={(event) => onChange(event.target.checked)} /><span><label htmlFor={id}><strong>{label}</strong></label><small id={`${id}-hint`}>{hint}</small></span></div>
}

function parseJSONObject(value: string): Record<string, unknown> {
  const parsed: unknown = JSON.parse(value || '{}')
  if (!parsed || Array.isArray(parsed) || typeof parsed !== 'object') throw new Error('not object')
  return parsed as Record<string, unknown>
}

function parsePairs(value: string): Record<string, string> {
  return Object.fromEntries(value.split('\n').map((line) => line.split('=', 2).map((item) => item.trim())).filter(([key, item]) => key && item))
}

function editorFromDefinition(definition: Awaited<ReturnType<typeof getTask>>): EditorState {
  return {
    ...emptyEditor,
    name: definition.name,
    description: definition.description,
    scriptId: definition.scriptId,
    versionPolicy: definition.versionPolicy,
    pinnedVersionId: definition.pinnedVersionId || '',
    requiredRuntime: definition.requiredRuntime,
    parameters: JSON.stringify(definition.parameters, null, 2),
    secretRefs: formatPairs(definition.secretRefs),
    requiredLabels: formatPairs(definition.requiredLabels),
    cpuMillicores: definition.resources.cpuMillicores,
    memoryMB: Math.max(1, Math.round(definition.resources.memoryBytes / 1048576)),
    diskMB: Math.max(1, Math.round(definition.resources.diskBytes / 1048576)),
    priority: definition.priority,
    maxConcurrency: definition.maxConcurrency,
    timeoutSeconds: definition.timeoutSeconds,
    maxWaitSeconds: definition.maxWaitSeconds,
    maxRetries: definition.retryPolicy.maxRetries,
    backoffSeconds: definition.retryPolicy.backoffSeconds,
    idempotent: definition.idempotent,
    enabled: definition.enabled,
  }
}

function formatPairs(values: Record<string, string>) {
  return Object.entries(values || {}).map(([key, value]) => `${key}=${value}`).join('\n')
}
