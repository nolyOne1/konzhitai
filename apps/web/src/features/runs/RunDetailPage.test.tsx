import { withSession } from '../../test/session'
import { act, cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { RunDetailPage } from './RunDetailPage'
import { RunsPage } from './RunsPage'

const run = {
  id: 'run-1', definitionId: 'task-1', taskName: '每日归档任务', scriptId: 'script-1', scriptName: '归档脚本',
  scriptVersionId: 'version-3', versionNumber: 3, serverId: 'server-1', serverName: '京东云-华北-01',
  triggerType: 'manual', state: 'running', parameters: { 日期: '2026-08-28' },
  resources: { cpuMillicores: 500, memoryBytes: 536870912, diskBytes: 1073741824 }, requiredRuntime: 'python3',
  priority: 70, attempt: 1, maxRetries: 2, idempotent: true, processConfirmedGone: false, queuedAt: '2026-08-28T08:00:00Z',
  assignedAt: '2026-08-28T08:00:02Z', startedAt: '2026-08-28T08:00:03Z', resultSummary: '', createdAt: '2026-08-28T08:00:00Z',
}

describe('执行记录与实时日志', () => {
  it.each(['timed_out', 'cancelled'])('终止状态 %s 不将历史退出码 0 展示为成功', async (state) => {
    installEventSource()
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response({ ...run, state, exitCode: 0, finishedAt: '2026-08-28T08:00:09Z' })))
    render(withSession(<MemoryRouter initialEntries={['/runs/run-1']}><Routes><Route path="/runs/:id" element={<RunDetailPage />} /></Routes></MemoryRouter>))
    await waitFor(() => expect(screen.getAllByText(state === 'timed_out' ? '执行超时' : '任务已取消').length).toBeGreaterThan(0))
    expect(screen.queryByText('退出码 0')).not.toBeInTheDocument()
  })
  it('旧详情请求晚返回时不覆盖已完成结果', async () => {
    const source = installEventSource()
    let resolveOld!: (value: ReturnType<typeof response>) => void
    const old = new Promise<ReturnType<typeof response>>((resolve) => { resolveOld = resolve })
    vi.stubGlobal('fetch', vi.fn().mockReturnValueOnce(old)
      .mockResolvedValue(response({ ...run, state: 'succeeded', exitCode: 0, finishedAt: '2026-08-28T08:00:09Z' })))
    render(withSession(<MemoryRouter initialEntries={['/runs/run-1']}><Routes><Route path="/runs/:id" element={<RunDetailPage />} /></Routes></MemoryRouter>))
    act(() => source.emit('state', { id: 'done', kind: 'state', state: 'succeeded', sequence: 4, occurredAt: '2026-08-28T08:00:09Z' }))
    expect(await screen.findByText('退出码 0')).toBeVisible()
    await act(async () => { resolveOld(response({ ...run, state: 'queued', serverId: '', serverName: '' })); await old })
    expect(screen.getByText('退出码 0')).toBeVisible()
    expect(screen.getByText('京东云-华北-01')).toBeVisible()
    expect(screen.queryByText('等待自动分配')).not.toBeInTheDocument()
  })

  it('完成事件后自动刷新服务器和退出码，不需要手动刷新页面', async () => {
    const source = installEventSource()
    vi.stubGlobal('fetch', vi.fn()
      .mockResolvedValueOnce(response({ ...run, state: 'queued', serverId: '', serverName: '' }))
      .mockResolvedValue(response({ ...run, state: 'succeeded', exitCode: 0, finishedAt: '2026-08-28T08:00:09Z' })))
    render(withSession(<MemoryRouter initialEntries={['/runs/run-1']}><Routes><Route path="/runs/:id" element={<RunDetailPage />} /></Routes></MemoryRouter>))
    expect(await screen.findByText('等待自动分配')).toBeVisible()
    act(() => source.emit('state', { id: 'done', kind: 'state', state: 'succeeded', sequence: 4, occurredAt: '2026-08-28T08:00:09Z' }))
    expect(await screen.findByText('退出码 0')).toBeVisible()
    expect(screen.getByText('京东云-华北-01')).toBeVisible()
    expect(screen.queryByText('任务尚未结束')).not.toBeInTheDocument()
  })

  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
  })

  it('展示运行上下文并支持实时日志过滤、暂停和仅浏览器清屏', async () => {
    const source = installEventSource()
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response(run)))
    const user = userEvent.setup()
    render(withSession(<MemoryRouter initialEntries={['/runs/run-1']}><Routes><Route path="/runs/:id" element={<RunDetailPage />} /></Routes></MemoryRouter>))

    expect(await screen.findByRole('heading', { name: '每日归档任务' })).toBeVisible()
    expect(screen.getByText('京东云-华北-01')).toBeVisible()
    expect(screen.getByText(/^版本 3/)).toBeVisible()
    source.emit('state', { id: 'state-1', kind: 'state', state: 'running', eventType: 'run.started', sequence: 1, message: '任务已开始执行', occurredAt: '2026-08-28T08:00:03Z' })
    source.emit('log', { id: 'log-1', kind: 'log', stream: 'stdout', sequence: 1, content: '处理订单 1001\n', occurredAt: '2026-08-28T08:00:04Z' })
    source.emit('log', { id: 'log-2', kind: 'log', stream: 'stderr', sequence: 1, content: '警告：订单 1002\n', occurredAt: '2026-08-28T08:00:05Z' })
    expect(await screen.findByText(/处理订单 1001/)).toBeVisible()
    expect(screen.getByText(/警告：订单 1002/)).toBeVisible()

    await user.type(screen.getByLabelText('筛选日志关键词'), '警告')
    expect(screen.queryByText(/处理订单 1001/)).not.toBeInTheDocument()
    expect(screen.getByText(/警告：订单 1002/)).toBeVisible()
    await user.click(screen.getByRole('button', { name: '暂停自动滚动' }))
    expect(screen.getByRole('button', { name: '继续自动滚动' })).toBeVisible()
    await user.click(screen.getByRole('button', { name: '清空浏览器显示' }))
    expect(screen.getByText('浏览器显示已清空，服务端日志仍然保留。')).toBeVisible()
  })

  it('执行记录列表保持控制台表格并使用中文状态', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response({ runs: [run, { ...run, id: 'run-2', state: 'queued', serverId: '', serverName: '' }] })))
    render(withSession(<MemoryRouter><RunsPage /></MemoryRouter>))
    expect(await screen.findByRole('heading', { name: '执行记录' })).toBeVisible()
    expect(screen.getByText('运行中', { selector: '.run-state' })).toBeVisible()
    expect(screen.getByText('排队等待', { selector: '.run-state' })).toBeVisible()
    expect(screen.getByText('暂无分配服务器')).toBeVisible()
  })

  it('原进程未确认结束时禁止重试未知状态任务', async () => {
    installEventSource()
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response({ ...run, state: 'unknown', processConfirmedGone: false })))
    render(withSession(<MemoryRouter initialEntries={['/runs/run-1']}><Routes><Route path="/runs/:id" element={<RunDetailPage />} /></Routes></MemoryRouter>))

    expect(await screen.findByRole('button', { name: '安全重试' })).toBeDisabled()
    expect(screen.getByRole('button', { name: '按当前任务配置再次执行' })).toBeDisabled()
  })

  it('成功实例使用当前任务参数创建独立新执行，不消耗安全重试链', async () => {
    installEventSource()
    const fetchMock = vi.fn(async (path: string, init?: RequestInit) => {
      if (path === '/api/runs/run-1') return response({ ...run, state: 'succeeded', idempotent: false, maxRetries: 0 })
      if (path.endsWith('/archive/info')) return response({ available: false, current: false })
      if (path === '/api/tasks/task-1' && !init?.method) return response({ id: 'task-1', name: run.taskName, enabled: true, versionPolicy: 'latest', parameters: { 日期: '当前配置' } })
      if (path === '/api/tasks/task-1/run' && init?.method === 'POST') return response({ id: 'run-2' }, 201)
      throw new Error(path)
    })
    vi.stubGlobal('fetch', fetchMock)
    const user = userEvent.setup()
    render(withSession(<MemoryRouter initialEntries={['/runs/run-1']}><Routes><Route path="/runs/:id" element={<RunDetailPage />} /><Route path="/runs/run-2" element={<p>新执行已创建</p>} /></Routes></MemoryRouter>))
    expect(await screen.findByRole('button', { name: '安全重试' })).toBeDisabled()
    await user.click(screen.getByRole('button', { name: '按当前任务配置再次执行' }))
    expect(await screen.findByLabelText('本次普通参数（JSON）')).toHaveValue(JSON.stringify({ 日期: '当前配置' }, null, 2))
    await user.click(screen.getByRole('button', { name: '确认执行' }))
    expect(await screen.findByText('新执行已创建')).toBeVisible()
    const create = fetchMock.mock.calls.find(([path]) => path === '/api/tasks/task-1/run')
    expect(JSON.parse(create?.[1]?.body as string)).toEqual({ parameters: { 日期: '当前配置' } })
    expect(fetchMock.mock.calls.some(([path]) => path.endsWith('/retry'))).toBe(false)
  })

  it('展示资源采样，并通过SSE更新累计CPU而不把缺少字段当作零', async () => {
    const source = installEventSource()
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response(run)))
    render(withSession(<MemoryRouter initialEntries={['/runs/run-1']}><Routes><Route path="/runs/:id" element={<RunDetailPage />} /></Routes></MemoryRouter>))
    await screen.findByText('执行代理尚未上报资源用量。')
    act(() => source.emit('state', { id: 'usage-1', kind: 'state', sequence: 12, eventType: 'run.usage', usage: { cpuTimeMillis: 1234, memoryBytes: 1048576, sampledAt: '2026-08-28T08:00:06Z' }, occurredAt: '2026-08-28T08:00:06Z' }))
    expect(await screen.findByText('1.23 秒')).toBeVisible()
    expect(screen.getByText('1 MB')).toBeVisible()
    expect(screen.getByText('观测内存峰值').parentElement).toHaveTextContent('—')
    expect(screen.getByText('进程数').parentElement).toHaveTextContent('—')
  })

  it('只读成员可下载日志但不能取消或重新执行', async () => {
    installEventSource()
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response(run)))
    render(withSession(<MemoryRouter initialEntries={['/runs/run-1']}><Routes><Route path="/runs/:id" element={<RunDetailPage />} /></Routes></MemoryRouter>, ['viewer']))
    expect(await screen.findByRole('button', { name: '取消任务' })).toBeDisabled()
    expect(screen.getByRole('button', { name: '安全重试' })).toBeDisabled()
    expect(screen.getByRole('button', { name: '按当前任务配置再次执行' })).toBeDisabled()
    expect(screen.getByRole('button', { name: '下载日志' })).toBeEnabled()
  })

  it('从服务端下载完整日志、最新压缩归档和运行产物', async () => {
    installEventSource()
    const stored = new Blob(['server complete log'])
    const compressed = new Blob(['archive bytes'])
    const artifact = new Blob(['report bytes'])
    const createObjectURL = vi.fn().mockReturnValue('blob:download')
    class DownloadURL extends URL {
      static createObjectURL = createObjectURL
      static revokeObjectURL = vi.fn()
    }
    vi.stubGlobal('URL', DownloadURL)
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
    const fetchMock = vi.fn(async (path: string) => {
      if (path === '/api/runs/run-1') return response({ ...run, state: 'succeeded' })
      if (path.endsWith('/logs/archive/info')) return response({ available: true, current: true, byteSize: 1024 })
      if (path.endsWith('/logs/archive')) return { ...response(null), blob: async () => compressed }
      if (path.endsWith('/logs')) return { ...response(null), blob: async () => stored }
      if (path.endsWith('/artifacts')) return response({ artifacts: [{ id: 'artifact-1', name: 'report.csv', byteSize: 12, sha256: 'a'.repeat(64), createdAt: '2026-08-28T08:00:07Z' }] })
      if (path.endsWith('/artifacts/artifact-1')) return { ...response(null), blob: async () => artifact }
      throw new Error(path)
    })
    vi.stubGlobal('fetch', fetchMock)
    const user = userEvent.setup()
    render(withSession(<MemoryRouter initialEntries={['/runs/run-1']}><Routes><Route path="/runs/:id" element={<RunDetailPage />} /></Routes></MemoryRouter>))
    await screen.findByRole('button', { name: '下载压缩归档' })
    await user.click(screen.getByRole('button', { name: '下载日志' }))
    expect(createObjectURL).toHaveBeenCalledWith(stored)
    await user.click(screen.getByRole('button', { name: '下载压缩归档' }))
    expect(createObjectURL).toHaveBeenCalledWith(compressed)
    await user.click(screen.getByRole('button', { name: '下载 report.csv' }))
    expect(createObjectURL).toHaveBeenCalledWith(artifact)
    expect(click).toHaveBeenCalledTimes(3)
  })
})

function response(value: unknown, status = 200) {
  return { ok: status >= 200 && status < 300, status, json: async () => value }
}

function installEventSource() {
  const listeners = new Map<string, (event: MessageEvent) => void>()
  class FakeEventSource {
    addEventListener(type: string, listener: EventListener) { listeners.set(type, listener as (event: MessageEvent) => void) }
    close() {}
  }
  vi.stubGlobal('EventSource', FakeEventSource)
  return {
    emit(type: string, value: unknown) {
      const listener = listeners.get(type)
      if (!listener) throw new Error(`未注册 ${type} 事件`)
      listener(new MessageEvent(type, { data: JSON.stringify(value) }))
    },
  }
}
