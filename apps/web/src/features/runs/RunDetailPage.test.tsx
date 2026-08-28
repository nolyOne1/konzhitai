import { cleanup, render, screen, waitFor } from '@testing-library/react'
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
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it('展示运行上下文并支持实时日志过滤、暂停和仅浏览器清屏', async () => {
    const source = installEventSource()
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response(run)))
    const user = userEvent.setup()
    render(<MemoryRouter initialEntries={['/runs/run-1']}><Routes><Route path="/runs/:id" element={<RunDetailPage />} /></Routes></MemoryRouter>)

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
    render(<MemoryRouter><RunsPage /></MemoryRouter>)
    expect(await screen.findByRole('heading', { name: '执行记录' })).toBeVisible()
    expect(screen.getByText('运行中', { selector: '.run-state' })).toBeVisible()
    expect(screen.getByText('排队等待', { selector: '.run-state' })).toBeVisible()
    expect(screen.getByText('暂无分配服务器')).toBeVisible()
  })

  it('原进程未确认结束时禁止重试未知状态任务', async () => {
    installEventSource()
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response({ ...run, state: 'unknown', processConfirmedGone: false })))
    render(<MemoryRouter initialEntries={['/runs/run-1']}><Routes><Route path="/runs/:id" element={<RunDetailPage />} /></Routes></MemoryRouter>)

    expect(await screen.findByRole('button', { name: '重新执行' })).toBeDisabled()
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
