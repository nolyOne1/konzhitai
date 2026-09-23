import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, expect, it, vi } from 'vitest'

import { RunsPage } from './RunsPage'

afterEach(() => { cleanup(); vi.unstubAllGlobals() })

it('把时间和状态条件发到服务端，并可翻到历史下一页', async () => {
  const fetchMock = vi.fn(async (path: string) => {
    if (path === '/api/tasks') return response({ tasks: [] })
    if (path === '/api/scripts') return response({ scripts: [] })
    if (path === '/api/servers') return response({ servers: [] })
    if (path.startsWith('/api/runs?')) return response({ runs: [], hasMore: true, limit: 50, offset: 0 })
    throw new Error(path)
  })
  vi.stubGlobal('fetch', fetchMock)
  const user = userEvent.setup()
  render(<MemoryRouter><RunsPage /></MemoryRouter>)
  await waitFor(() => expect(screen.getByRole('button', { name: '查询记录' })).toBeEnabled())
  await user.type(screen.getByLabelText('筛选执行记录'), '历史订单')
  await user.selectOptions(screen.getByLabelText('筛选运行状态'), 'failed')
  fireEvent.change(screen.getByLabelText('开始时间'), { target: { value: '2026-08-01T00:00' } })
  fireEvent.change(screen.getByLabelText('结束时间（不含）'), { target: { value: '2026-08-02T00:00' } })
  await user.click(screen.getByRole('button', { name: '查询记录' }))
  await waitFor(() => expect(fetchMock.mock.calls.some(([path]) => {
    const params = new URL(path, 'http://localhost').searchParams
    return params.get('query') === '历史订单' && params.get('state') === 'failed' && Boolean(params.get('from')) && Boolean(params.get('until')) && params.get('offset') === '0'
  })).toBe(true))
  await user.click(screen.getByRole('button', { name: '下一页' }))
  await waitFor(() => expect(fetchMock.mock.calls.some(([path]) => path.includes('offset=50') && path.includes('state=failed'))).toBe(true))
  expect(screen.getByRole('button', { name: '上一页' })).toBeEnabled()
})

function response(value: unknown) { return { ok: true, status: 200, json: async () => value } }
