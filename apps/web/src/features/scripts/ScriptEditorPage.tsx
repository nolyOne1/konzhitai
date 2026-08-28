import { useEffect, useMemo, useRef, useState } from 'react'
import { Link, useParams } from 'react-router-dom'

import {
  getScript,
  getScriptVersionContent,
  publishScript,
  rollbackScript,
  saveScriptDraft,
  type DistributionMode,
  type ParameterDefinition,
  type ScriptDetail,
  type ScriptEditorInput,
  type ScriptVersion,
} from '../../api/client'
import { SyncStatusPanel } from './SyncStatusPanel'

type EditorState = {
  content: string
  runtime: string
  entrypoint: string
  category: string
  tags: string
  distributionMode: DistributionMode
  serverGroupId: string
  labelRules: string
  parameters: ParameterDefinition[]
  cpuMillicores: number
  memoryMB: number
  diskMB: number
}

const emptyEditor: EditorState = {
  content: '',
  runtime: 'bash',
  entrypoint: 'main.sh',
  category: '未分类',
  tags: '',
  distributionMode: 'on_demand',
  serverGroupId: '',
  labelRules: '',
  parameters: [],
  cpuMillicores: 100,
  memoryMB: 128,
  diskMB: 128,
}

export function ScriptEditorPage() {
  const { id = '' } = useParams()
  const [detail, setDetail] = useState<ScriptDetail | null>(null)
  const [editor, setEditor] = useState<EditorState>(emptyEditor)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [status, setStatus] = useState('')
  const [error, setError] = useState('')
  const [showPublish, setShowPublish] = useState(false)
  const [releaseNotes, setReleaseNotes] = useState('')
  const [publishError, setPublishError] = useState('')
  const [compare, setCompare] = useState<{ version: ScriptVersion; content: string } | null>(null)
  const [compareLoading, setCompareLoading] = useState('')
  const [rollbackVersion, setRollbackVersion] = useState<ScriptVersion | null>(null)
  const [rollbackNotes, setRollbackNotes] = useState('')
  const [rollbackError, setRollbackError] = useState('')
  const publishButtonRef = useRef<HTMLButtonElement>(null)
  const releaseNotesRef = useRef<HTMLTextAreaElement>(null)

  useEffect(() => {
    let active = true
    setLoading(true)
    getScript(id)
      .then((value) => {
        if (!active) return
        setDetail(value)
        setEditor(editorFromDetail(value))
      })
      .catch((reason: unknown) => {
        if (active) setError(reason instanceof Error ? reason.message : '脚本加载失败')
      })
      .finally(() => {
        if (active) setLoading(false)
      })
    return () => { active = false }
  }, [id])

  useEffect(() => {
    if (showPublish) releaseNotesRef.current?.focus()
  }, [showPublish])

  const editorInput = useMemo((): ScriptEditorInput => ({
    content: editor.content,
    runtime: editor.runtime,
    entrypoint: editor.entrypoint.trim(),
    category: editor.category.trim() || '未分类',
    tags: parseTags(editor.tags),
    distribution: {
      mode: editor.distributionMode,
      serverGroupId: editor.distributionMode === 'server_group' ? editor.serverGroupId.trim() : undefined,
      labels: editor.distributionMode === 'labels' ? parseLabels(editor.labelRules) : {},
    },
    parameterDefinitions: editor.parameters,
    resources: {
      cpuMillicores: Math.max(1, editor.cpuMillicores),
      memoryBytes: Math.max(1, editor.memoryMB) * 1048576,
      diskBytes: Math.max(1, editor.diskMB) * 1048576,
    },
  }), [editor])

  async function saveDraft() {
    setSaving(true)
    setError('')
    setStatus('')
    try {
      await saveScriptDraft(id, editorInput)
      setStatus('草稿已保存')
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '保存草稿失败')
    } finally {
      setSaving(false)
    }
  }

  async function confirmPublish() {
    if (!containsChinese(releaseNotes)) {
      setPublishError('发布说明必须包含中文，便于团队理解本次变更。')
      releaseNotesRef.current?.focus()
      return
    }
    setSaving(true)
    setPublishError('')
    try {
      const version = await publishScript(id, { ...editorInput, releaseNotes: releaseNotes.trim() })
      setDetail((current) => current ? {
        ...current,
        script: { ...current.script, currentVersion: version.number, currentVersionId: version.id },
        versions: [version, ...current.versions],
      } : current)
      setShowPublish(false)
      setReleaseNotes('')
      setStatus(`版本 ${version.number} 已发布`)
      queueMicrotask(() => publishButtonRef.current?.focus())
    } catch (reason) {
      setPublishError(reason instanceof Error ? reason.message : '发布脚本失败')
    } finally {
      setSaving(false)
    }
  }

  async function openCompare(version: ScriptVersion) {
    setCompareLoading(version.id)
    setError('')
    try {
      const content = await getScriptVersionContent(id, version.id)
      setCompare({ version, content })
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '读取版本内容失败')
    } finally {
      setCompareLoading('')
    }
  }

  async function confirmRollback() {
    if (!rollbackVersion || !containsChinese(rollbackNotes)) {
      setRollbackError('回滚说明必须包含中文。')
      return
    }
    setSaving(true)
    setRollbackError('')
    try {
      const historicalContent = await getScriptVersionContent(id, rollbackVersion.id)
      const version = await rollbackScript(id, rollbackVersion.id, rollbackNotes.trim())
      setDetail((current) => current ? {
        ...current,
        script: { ...current.script, currentVersion: version.number, currentVersionId: version.id },
        versions: [version, ...current.versions],
      } : current)
      setEditor(editorFromVersion(version, historicalContent))
      setRollbackVersion(null)
      setRollbackNotes('')
      setStatus(`已回滚并发布为版本 ${version.number}`)
    } catch (reason) {
      setRollbackError(reason instanceof Error ? reason.message : '回滚脚本失败')
    } finally {
      setSaving(false)
    }
  }

  if (loading) return <div className="page-loading" aria-live="polite">正在加载脚本编辑器…</div>
  if (!detail) return <div className="notice notice-error" role="alert">{error || '脚本不存在'}</div>

  return (
    <>
      <div className="editor-page-heading">
        <div>
          <Link className="back-link" to="/scripts">← 返回脚本中心</Link>
          <p className="eyebrow">脚本开发工作区</p>
          <h1>编辑{detail.script.name}</h1>
          <p>{detail.script.description || '编辑内容、资源要求和发布范围，保存后再生成不可变版本。'}</p>
        </div>
        <div className="editor-heading-actions">
          <span className="draft-state" aria-live="polite">{status || `当前版本 ${detail.script.currentVersion || '未发布'}`}</span>
          <button type="button" className="secondary-action" disabled={saving} onClick={() => void saveDraft()}>保存草稿</button>
          <button ref={publishButtonRef} type="button" className="primary-action" disabled={saving} onClick={() => { setPublishError(''); setShowPublish(true) }}>发布版本</button>
        </div>
      </div>

      {error && <div className="notice notice-error" role="alert">{error}</div>}

      <div className="script-editor-layout">
        <section className="code-workspace" aria-label="脚本代码编辑区">
          <header className="code-toolbar">
            <div><h2 id="content-title">脚本内容</h2><span>{editor.entrypoint || '未设置入口文件'}</span></div>
            <span className="runtime-pill">{runtimeLabel(editor.runtime)}</span>
          </header>
          <label className="sr-only" htmlFor="script-content">脚本内容</label>
          <textarea
            id="script-content"
            className="code-editor"
            value={editor.content}
            spellCheck={false}
            onChange={(event) => { setEditor({ ...editor, content: event.target.value }); setStatus('有未保存更改') }}
          />
          <footer className="code-statusbar"><span>{countLines(editor.content)} 行</span><span>UTF-8 · LF</span></footer>
        </section>

        <aside className="script-settings" aria-label="脚本配置">
          <SettingsSection title="基础信息">
            <label className="form-field">运行环境
              <select value={editor.runtime} onChange={(event) => setEditor({ ...editor, runtime: event.target.value })}>
                <option value="bash">Shell / Bash</option><option value="python3">Python 3</option><option value="node">Node.js</option><option value="powershell">PowerShell</option>
              </select>
            </label>
            <label className="form-field">入口文件<input value={editor.entrypoint} onChange={(event) => setEditor({ ...editor, entrypoint: event.target.value })} /></label>
            <label className="form-field">分类<input value={editor.category} onChange={(event) => setEditor({ ...editor, category: event.target.value })} /></label>
            <label className="form-field">标签<input value={editor.tags} placeholder="生产，每日" onChange={(event) => setEditor({ ...editor, tags: event.target.value })} /><small>使用逗号分隔，便于团队检索。</small></label>
          </SettingsSection>

          <SettingsSection title="发布与同步">
            <label className="form-field">发布目标
              <select aria-label="发布目标" value={editor.distributionMode} onChange={(event) => setEditor({ ...editor, distributionMode: event.target.value as DistributionMode })}>
                <option value="all_compatible">全部兼容服务器</option><option value="server_group">指定服务器组</option><option value="labels">指定标签集合</option><option value="on_demand">按需分发</option>
              </select>
            </label>
            {editor.distributionMode === 'server_group' && <label className="form-field">服务器组标识<input value={editor.serverGroupId} onChange={(event) => setEditor({ ...editor, serverGroupId: event.target.value })} /></label>}
            {editor.distributionMode === 'labels' && <label className="form-field">标签规则<textarea className="compact-textarea" value={editor.labelRules} placeholder={'用途=批处理\n环境=生产'} onChange={(event) => setEditor({ ...editor, labelRules: event.target.value })} /><small>每行一个“键=值”条件。</small></label>}
            <p className="settings-hint">发布后由代理校验脚本包；同步失败不会替换服务器上的旧版本。</p>
          </SettingsSection>

          <SettingsSection title="资源要求">
            <div className="resource-input-grid">
              <NumberField label="CPU（毫核）" value={editor.cpuMillicores} onChange={(value) => setEditor({ ...editor, cpuMillicores: value })} />
              <NumberField label="内存（MB）" value={editor.memoryMB} onChange={(value) => setEditor({ ...editor, memoryMB: value })} />
              <NumberField label="磁盘（MB）" value={editor.diskMB} onChange={(value) => setEditor({ ...editor, diskMB: value })} />
            </div>
          </SettingsSection>

          <SettingsSection title="参数定义" action={<button type="button" className="text-action" onClick={() => setEditor({ ...editor, parameters: [...editor.parameters, { name: '', type: 'string', required: false }] })}>添加参数</button>}>
            {editor.parameters.length === 0 ? <p className="settings-hint">暂无参数。任务创建时会按这里的类型定义传值。</p> : editor.parameters.map((parameter, index) => (
              <div className="parameter-row" key={index}>
                <input aria-label={`参数 ${index + 1} 名称`} value={parameter.name} placeholder="参数名" onChange={(event) => updateParameter(index, { ...parameter, name: event.target.value }, editor, setEditor)} />
                <select aria-label={`参数 ${index + 1} 类型`} value={parameter.type} onChange={(event) => updateParameter(index, { ...parameter, type: event.target.value }, editor, setEditor)}><option value="string">文本</option><option value="number">数字</option><option value="boolean">布尔值</option></select>
                <button type="button" aria-label={`删除参数 ${index + 1}`} onClick={() => setEditor({ ...editor, parameters: editor.parameters.filter((_, itemIndex) => itemIndex !== index) })}>×</button>
              </div>
            ))}
          </SettingsSection>
        </aside>
      </div>

      <SyncStatusPanel scriptId={id} refreshKey={detail.script.currentVersionId} />

      <section className="panel version-panel" aria-labelledby="version-title">
        <header className="panel-header"><div><h2 id="version-title">版本历史</h2><p>版本只追加，不覆盖；回滚也会生成一个新的发布版本。</p></div><span>{detail.versions.length} 个版本</span></header>
        {detail.versions.length === 0 ? <div className="large-empty compact-version-empty"><h3>尚未发布版本</h3><p>保存草稿后填写中文发布说明，即可生成第一个不可变版本。</p></div> : (
          <ol className="version-list">
            {detail.versions.map((version) => (
              <li key={version.id}>
                <div className="version-number"><strong>版本 {version.number}</strong><span>{formatDate(version.createdAt)}</span></div>
                <div className="version-notes"><strong>{version.releaseNotes}</strong><span>{version.entrypoint} · {shortHash(version.artifactSha256)}</span></div>
                <div className="row-actions">
                  <button type="button" disabled={compareLoading === version.id} aria-label={`与草稿比较版本 ${version.number}`} onClick={() => void openCompare(version)}>{compareLoading === version.id ? '读取中' : '与草稿比较'}</button>
                  <button type="button" onClick={() => { setRollbackVersion(version); setRollbackError(''); setRollbackNotes('') }}>回滚到此版本</button>
                </div>
              </li>
            ))}
          </ol>
        )}
      </section>

      {showPublish && <PublishDialog saving={saving} notes={releaseNotes} error={publishError} notesRef={releaseNotesRef} target={distributionLabel(editor.distributionMode)} onNotes={setReleaseNotes} onClose={() => { setShowPublish(false); queueMicrotask(() => publishButtonRef.current?.focus()) }} onConfirm={() => void confirmPublish()} />}
      {compare && <CompareDialog version={compare.version} historical={compare.content} draft={editor.content} onClose={() => setCompare(null)} />}
      {rollbackVersion && <RollbackDialog version={rollbackVersion} notes={rollbackNotes} error={rollbackError} saving={saving} onNotes={setRollbackNotes} onClose={() => setRollbackVersion(null)} onConfirm={() => void confirmRollback()} />}
    </>
  )
}

function SettingsSection({ title, action, children }: { title: string; action?: React.ReactNode; children: React.ReactNode }) {
  return <section className="settings-section"><header><h2>{title}</h2>{action}</header>{children}</section>
}

function NumberField({ label, value, onChange }: { label: string; value: number; onChange: (value: number) => void }) {
  return <label className="form-field">{label}<input type="number" min="1" inputMode="numeric" value={value} onChange={(event) => onChange(Number(event.target.value))} /></label>
}

function PublishDialog({ saving, notes, error, notesRef, target, onNotes, onClose, onConfirm }: {
  saving: boolean; notes: string; error: string; notesRef: React.RefObject<HTMLTextAreaElement | null>; target: string
  onNotes: (value: string) => void; onClose: () => void; onConfirm: () => void
}) {
  return <div className="drawer-backdrop centered-dialog"><section className="console-dialog" role="dialog" aria-modal="true" aria-labelledby="publish-title" onKeyDown={(event) => handleDialogKeys(event, onClose)}><header className="drawer-header"><div><p className="eyebrow">生成不可变版本</p><h2 id="publish-title">发布脚本版本</h2><p>目标：{target}</p></div><button type="button" className="icon-button" aria-label="关闭发布窗口" onClick={onClose}>×</button></header>{error && <div className="form-error" role="alert">{error}</div>}<label className="form-field">中文发布说明<textarea ref={notesRef} value={notes} placeholder="例如：增加归档前的数据校验" onChange={(event) => onNotes(event.target.value)} /></label><p className="dialog-hint">发布会保存脚本包校验值，历史版本不会被修改。</p><footer className="dialog-actions"><button type="button" className="secondary-action" onClick={onClose}>取消</button><button type="button" className="primary-action" disabled={saving} onClick={onConfirm}>确认发布</button></footer></section></div>
}

function CompareDialog({ version, historical, draft, onClose }: { version: ScriptVersion; historical: string; draft: string; onClose: () => void }) {
  const closeRef = useRef<HTMLButtonElement>(null)
  useEffect(() => { closeRef.current?.focus() }, [])
  return <div className="drawer-backdrop centered-dialog"><section className="console-dialog compare-dialog" role="dialog" aria-modal="true" aria-labelledby="compare-title" onKeyDown={(event) => handleDialogKeys(event, onClose)}><header className="drawer-header"><div><p className="eyebrow">只读比较</p><h2 id="compare-title">版本对比</h2><p>版本 {version.number} 与当前草稿</p></div><button ref={closeRef} type="button" className="icon-button" aria-label="关闭版本对比" onClick={onClose}>×</button></header><div className="compare-grid"><div><strong>版本 {version.number}</strong><pre>{historical}</pre></div><div><strong>当前草稿</strong><pre>{draft}</pre></div></div></section></div>
}

function RollbackDialog({ version, notes, error, saving, onNotes, onClose, onConfirm }: { version: ScriptVersion; notes: string; error: string; saving: boolean; onNotes: (value: string) => void; onClose: () => void; onConfirm: () => void }) {
  const notesRef = useRef<HTMLTextAreaElement>(null)
  useEffect(() => { notesRef.current?.focus() }, [])
  return <div className="drawer-backdrop centered-dialog"><section className="console-dialog" role="dialog" aria-modal="true" aria-labelledby="rollback-title" onKeyDown={(event) => handleDialogKeys(event, onClose)}><header className="drawer-header"><div><p className="eyebrow">追加发布</p><h2 id="rollback-title">回滚到版本 {version.number}</h2><p>旧版本保持不变，历史内容将发布为新版本。</p></div><button type="button" className="icon-button" aria-label="关闭回滚窗口" onClick={onClose}>×</button></header>{error && <div className="form-error" role="alert">{error}</div>}<label className="form-field">中文回滚说明<textarea ref={notesRef} value={notes} onChange={(event) => onNotes(event.target.value)} /></label><footer className="dialog-actions"><button type="button" className="secondary-action" onClick={onClose}>取消</button><button type="button" className="primary-action" disabled={saving} onClick={onConfirm}>确认回滚并发布</button></footer></section></div>
}

function editorFromDetail(detail: ScriptDetail): EditorState {
  const manifest = detail.draft.manifest
  return {
    content: detail.draft.content,
    runtime: manifest.runtime,
    entrypoint: manifest.entrypoint,
    category: manifest.category || detail.script.category || '未分类',
    tags: (manifest.tags || detail.script.tags || []).join('，'),
    distributionMode: manifest.distribution?.mode || 'on_demand',
    serverGroupId: manifest.distribution?.serverGroupId || '',
    labelRules: Object.entries(manifest.distribution?.labels || {}).map(([key, value]) => `${key}=${value}`).join('\n'),
    parameters: manifest.parameterDefinitions || [],
    cpuMillicores: manifest.resources?.cpuMillicores || 100,
    memoryMB: Math.max(1, Math.round((manifest.resources?.memoryBytes || 134217728) / 1048576)),
    diskMB: Math.max(1, Math.round((manifest.resources?.diskBytes || 134217728) / 1048576)),
  }
}

function editorFromVersion(version: ScriptVersion, content: string): EditorState {
  const manifest = version.manifest
  return {
    content,
    runtime: manifest.runtime,
    entrypoint: manifest.entrypoint,
    category: manifest.category || '未分类',
    tags: (manifest.tags || []).join('，'),
    distributionMode: manifest.distribution?.mode || 'on_demand',
    serverGroupId: manifest.distribution?.serverGroupId || '',
    labelRules: Object.entries(manifest.distribution?.labels || {}).map(([key, value]) => `${key}=${value}`).join('\n'),
    parameters: manifest.parameterDefinitions || [],
    cpuMillicores: manifest.resources?.cpuMillicores || 100,
    memoryMB: Math.max(1, Math.round((manifest.resources?.memoryBytes || 134217728) / 1048576)),
    diskMB: Math.max(1, Math.round((manifest.resources?.diskBytes || 134217728) / 1048576)),
  }
}

function updateParameter(index: number, parameter: ParameterDefinition, editor: EditorState, setEditor: (value: EditorState) => void) {
  setEditor({ ...editor, parameters: editor.parameters.map((item, itemIndex) => itemIndex === index ? parameter : item) })
}

function parseTags(value: string) {
  return [...new Set(value.split(/[，,]/).map((tag) => tag.trim()).filter(Boolean))]
}

function parseLabels(value: string) {
  return Object.fromEntries(value.split('\n').map((line) => line.split('=', 2).map((part) => part.trim())).filter(([key, itemValue]) => key && itemValue))
}

function containsChinese(value: string) {
  return /[\u3400-\u9fff]/u.test(value)
}

function runtimeLabel(runtime: string) {
  return ({ bash: 'Shell / Bash', python3: 'Python 3', node: 'Node.js', powershell: 'PowerShell' } as Record<string, string>)[runtime] || runtime
}

function distributionLabel(mode: DistributionMode) {
  return ({ all_compatible: '全部兼容服务器', server_group: '指定服务器组', labels: '指定标签集合', on_demand: '按需分发' } as Record<DistributionMode, string>)[mode]
}

function countLines(value: string) {
  return value === '' ? 1 : value.split('\n').length
}

function shortHash(value: string) {
  return value ? `${value.slice(0, 8)}…` : '等待生成校验值'
}

function formatDate(value: string) {
  return new Intl.DateTimeFormat('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' }).format(new Date(value))
}

function handleDialogKeys(event: React.KeyboardEvent<HTMLElement>, onClose: () => void) {
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
