import { useDeferredValue, useEffect, useMemo, useRef, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'

import { createScript, getScripts, importScript, type ScriptView } from '../../api/client'

export function ScriptsPage() {
  const navigate = useNavigate()
  const [scripts, setScripts] = useState<ScriptView[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [search, setSearch] = useState('')
  const [runtime, setRuntime] = useState('all')
  const [showCreate, setShowCreate] = useState(false)
  const [creating, setCreating] = useState(false)
  const [formError, setFormError] = useState('')
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [newRuntime, setNewRuntime] = useState('bash')
  const searchValue = useDeferredValue(search.trim().toLowerCase())
  const fileInputRef = useRef<HTMLInputElement>(null)
  const nameInputRef = useRef<HTMLInputElement>(null)
  const createButtonRef = useRef<HTMLButtonElement>(null)

  useEffect(() => {
    let active = true
    getScripts()
      .then((value) => { if (active) setScripts(value) })
      .catch((reason: unknown) => { if (active) setError(reason instanceof Error ? reason.message : '脚本列表加载失败') })
      .finally(() => { if (active) setLoading(false) })
    return () => { active = false }
  }, [])

  useEffect(() => {
    if (showCreate) nameInputRef.current?.focus()
  }, [showCreate])

  const visibleScripts = useMemo(() => scripts.filter((script) => {
    const matchesRuntime = runtime === 'all' || script.runtime === runtime
    const haystack = `${script.name} ${script.description} ${script.category} ${script.tags.join(' ')}`.toLowerCase()
    return matchesRuntime && (!searchValue || haystack.includes(searchValue))
  }), [runtime, scripts, searchValue])

  const counts = {
    total: scripts.length,
    published: scripts.filter((script) => script.currentVersion > 0).length,
    drafts: scripts.filter((script) => script.currentVersion === 0).length,
    runtimes: new Set(scripts.map((script) => script.runtime)).size,
  }

  async function submitCreate(event: React.FormEvent) {
    event.preventDefault()
    if (!name.trim()) {
      setFormError('请填写脚本名称。')
      nameInputRef.current?.focus()
      return
    }
    setCreating(true)
    setFormError('')
    try {
      const created = await createScript({ name: name.trim(), description: description.trim(), runtime: newRuntime, category: '未分类', tags: [] })
      navigate(`/scripts/${created.id}`)
    } catch (reason) {
      setFormError(reason instanceof Error ? reason.message : '创建脚本失败')
    } finally {
      setCreating(false)
    }
  }

  async function importFile(file: File | undefined) {
    if (!file) return
    setError('')
    try {
      const created = await importScript(file)
      navigate(`/scripts/${created.id}`)
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '导入脚本失败')
      if (fileInputRef.current) fileInputRef.current.value = ''
    }
  }

  function closeCreate() {
    setShowCreate(false)
    queueMicrotask(() => createButtonRef.current?.focus())
  }

  return (
    <>
      <div className="page-heading">
        <div><p className="eyebrow">中央脚本仓库</p><h1>脚本中心</h1><p>统一维护草稿、不可变版本、运行环境与跨服务器发布范围</p></div>
        <div className="heading-actions">
          <input ref={fileInputRef} className="sr-only" id="script-file" type="file" accept=".sh,.py,.js,.ps1,text/plain" onChange={(event) => void importFile(event.target.files?.[0])} />
          <button type="button" className="secondary-action" onClick={() => fileInputRef.current?.click()}>导入脚本文件</button>
          <button ref={createButtonRef} type="button" className="primary-action" onClick={() => { setFormError(''); setShowCreate(true) }}>新建脚本</button>
        </div>
      </div>

      {error && <div className="notice notice-error" role="alert">{error}</div>}

      <section className="script-summary" aria-label="脚本统计">
        <Summary label="脚本总数" value={counts.total} /><Summary label="已发布" value={counts.published} /><Summary label="仅有草稿" value={counts.drafts} /><Summary label="运行环境" value={counts.runtimes} />
      </section>

      <section className="panel script-center-panel" aria-labelledby="script-list-title">
        <header className="script-list-toolbar">
          <div><h2 id="script-list-title">脚本仓库</h2><p>中央版本是纳管服务器执行脚本的唯一可信来源。</p></div>
          <div className="script-filters">
            <label><span className="sr-only">搜索脚本</span><input type="search" placeholder="搜索名称、分类或标签" value={search} onChange={(event) => setSearch(event.target.value)} /></label>
            <label><span className="sr-only">筛选运行环境</span><select value={runtime} onChange={(event) => setRuntime(event.target.value)}><option value="all">全部运行环境</option><option value="bash">Shell / Bash</option><option value="python3">Python 3</option><option value="node">Node.js</option><option value="powershell">PowerShell</option></select></label>
          </div>
        </header>

        {loading ? <div className="compact-empty" aria-live="polite"><span aria-hidden="true" />正在读取脚本…</div> : visibleScripts.length === 0 ? (
          <div className="large-empty"><span className="empty-script-mark" aria-hidden="true">&lt;/&gt;</span><h3>{scripts.length === 0 ? '还没有脚本' : '没有符合条件的脚本'}</h3><p>{scripts.length === 0 ? '新建或导入文本脚本，保存草稿后即可发布第一个不可变版本。' : '请调整搜索词或运行环境筛选。'}</p></div>
        ) : (
          <div className="table-scroll"><table className="data-table script-table"><thead><tr><th>脚本</th><th>分类与标签</th><th>运行环境</th><th>当前版本</th><th>最近更新</th><th>操作</th></tr></thead><tbody>{visibleScripts.map((script) => (
            <tr key={script.id}>
              <td data-label="脚本"><Link className="script-name-link" to={`/scripts/${script.id}`}><strong>{script.name}</strong><span>{script.description || '暂无说明'}</span></Link></td>
              <td data-label="分类与标签"><strong className="cell-category">{script.category || '未分类'}</strong><div className="tag-list">{script.tags.length === 0 ? <span>无标签</span> : script.tags.map((tag) => <span key={tag}>{tag}</span>)}</div></td>
              <td data-label="运行环境"><span className="runtime-pill">{runtimeLabel(script.runtime)}</span></td>
              <td data-label="当前版本">{script.currentVersion > 0 ? <span className="version-badge">版本 {script.currentVersion}</span> : <span className="status-badge">仅草稿</span>}</td>
              <td data-label="最近更新"><time>{formatDate(script.updatedAt)}</time></td>
              <td data-label="操作"><div className="row-actions"><Link to={`/scripts/${script.id}`}>编辑与发布</Link></div></td>
            </tr>
          ))}</tbody></table></div>
        )}
      </section>

      {showCreate && <div className="drawer-backdrop centered-dialog"><form className="console-dialog" role="dialog" aria-modal="true" aria-labelledby="create-script-title" onKeyDown={(event) => handleDialogKeys(event, closeCreate)} onSubmit={(event) => void submitCreate(event)}><header className="drawer-header"><div><p className="eyebrow">建立中央草稿</p><h2 id="create-script-title">新建脚本</h2><p>创建后进入浏览器编辑器配置内容与发布目标。</p></div><button type="button" className="icon-button" aria-label="关闭新建脚本窗口" onClick={closeCreate}>×</button></header>{formError && <div className="form-error" role="alert">{formError}</div>}<label className="form-field">脚本名称<input ref={nameInputRef} value={name} placeholder="例如：每日数据归档" onChange={(event) => setName(event.target.value)} /></label><label className="form-field">用途说明<textarea value={description} placeholder="说明脚本负责的业务和使用边界" onChange={(event) => setDescription(event.target.value)} /></label><label className="form-field">运行环境<select value={newRuntime} onChange={(event) => setNewRuntime(event.target.value)}><option value="bash">Shell / Bash</option><option value="python3">Python 3</option><option value="node">Node.js</option><option value="powershell">PowerShell</option></select></label><footer className="dialog-actions"><button type="button" className="secondary-action" onClick={closeCreate}>取消</button><button type="submit" className="primary-action" disabled={creating}>{creating ? '正在创建…' : '创建并编辑'}</button></footer></form></div>}
    </>
  )
}

function Summary({ label, value }: { label: string; value: number }) {
  return <div><span>{label}</span><strong>{value}</strong></div>
}

function runtimeLabel(runtime: string) {
  return ({ bash: 'Shell / Bash', python3: 'Python 3', node: 'Node.js', powershell: 'PowerShell' } as Record<string, string>)[runtime] || runtime
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
