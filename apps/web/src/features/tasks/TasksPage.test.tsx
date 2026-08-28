import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { TasksPage } from './TasksPage'

const definition = {
  id: 'task-1', name: '每日归档任务', description: '归档每日业务数据', scriptId: 'script-1', scriptName: '每日数据归档',
  versionPolicy: 'latest', parameters: {}, secretRefs: {}, priority: 70, requiredLabels: { 用途: '批处理' },
  requiredRuntime: 'bash', resources: { cpuMillicores: 250, memoryBytes: 268435456, diskBytes: 536870912 },
  maxConcurrency: 2, timeoutSeconds: 900, maxWaitSeconds: 3600, retryPolicy: { maxRetries: 2, backoffSeconds: 30 },
  idempotent: true, enabled: true, createdAt: '2026-08-28T08:00:00Z', updatedAt: '2026-08-28T08:00:00Z',
}

describe('任务调度列表', () => {
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it('手动执行进入排队并在停用时默认保留已排队实例', async () => {
    const fetchMock = vi.fn().mockImplementation(async (path: string, init?: RequestInit) => {
      if (path === '/api/tasks' && !init?.method) return response({ tasks: [definition] })
      if (path === '/api/tasks/task-1/run' && init?.method === 'POST') return response({ id: 'run-1', state: 'queued' }, 201)
      if (path === '/api/tasks/task-1/enabled' && init?.method === 'PATCH') return emptyResponse(204)
      throw new Error(`未处理的请求：${path}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    const user = userEvent.setup()
    render(<MemoryRouter><TasksPage /></MemoryRouter>)

    expect(await screen.findByRole('heading', { name: '任务调度' })).toBeVisible()
    expect(screen.getByText('资源不足时保持排队，服务器空闲后自动尝试分配。')).toBeVisible()
    await user.click(screen.getByRole('button', { name: '手动执行每日归档任务' }))
    expect(await screen.findByRole('status')).toHaveTextContent('每日归档任务已进入排队队列')

    await user.click(screen.getByRole('button', { name: '停用每日归档任务' }))
    const cancelQueued = screen.getByLabelText('同时取消当前排队任务')
    expect(cancelQueued).not.toBeChecked()
    await user.click(screen.getByRole('button', { name: '确认停用' }))

    const disableCall = fetchMock.mock.calls.find((call) => call[0] === '/api/tasks/task-1/enabled')
    expect(JSON.parse(disableCall?.[1]?.body as string)).toEqual({ enabled: false, cancelQueued: false })
    expect(await screen.findByText('每日归档任务已停用')).toBeVisible()
  })
})

function response(value: unknown, status = 200) {
  return Promise.resolve({ ok: status >= 200 && status < 300, status, json: async () => value })
}

function emptyResponse(status: number) {
  return Promise.resolve({ ok: true, status, json: async () => { throw new Error('empty') } })
}
