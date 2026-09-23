import { MemoryRouter } from 'react-router-dom'
import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { AuditPage } from './AuditPage'

describe('系统设置与审计', () => {
  afterEach(() => { cleanup(); vi.unstubAllGlobals() })

  it('展示合并告警和只追加审计，并可确认告警', async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input)
      if (path === '/api/alerts' && !init?.method) return response({ alerts: [{ id: 'alert-1', resourceType: 'server', resourceId: 'server-1', code: 'agent_offline', severity: 'critical', title: '服务器离线', message: '代理已离线', status: 'open', occurrences: 3, firstOccurredAt: '2026-08-29T02:00:00Z', lastOccurredAt: '2026-08-29T02:04:00Z' }] })
      if (path === '/api/audit') return response({ events: [{ id: 'audit-1', actorId: 'admin-1', action: 'secret.create', targetType: 'secret', targetId: 'secret-1', details: { name: '生产令牌' }, ipAddress: '203.0.113.10', createdAt: '2026-08-29T02:05:00Z' }] })
      return { ok: true, status: 204, json: async () => ({}) } as Response
    })
    vi.stubGlobal('fetch', fetchMock)
    const user = userEvent.setup()
    render(<MemoryRouter><AuditPage /></MemoryRouter>)

    expect(await screen.findByText('服务器离线')).toBeVisible()
    expect(screen.getByText('合并 3 次')).toBeVisible()
    expect((await screen.findAllByText('创建敏感参数')).length).toBeGreaterThan(0)
    await user.click(screen.getByRole('button', { name: '确认服务器离线告警' }))
    expect(await screen.findByText('已确认')).toBeVisible()
  })

  it('关联运行链接在服务端限定目标而不是过滤最近一页', async () => {
    const fetchMock = vi.fn(async (path: string) => {
      if (path === '/api/alerts') return response({ alerts: [] })
      if (path === '/api/audit?targetType=run&targetId=run-42') return response({ events: [] })
      throw new Error(path)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<MemoryRouter initialEntries={['/settings?targetType=run&targetId=run-42']}><AuditPage /></MemoryRouter>)
    expect(await screen.findByText('暂无审计记录')).toBeVisible()
    expect(screen.getByRole('status')).toHaveTextContent('run / run-42')
    expect(screen.getByRole('link', { name: '查看全部审计' })).toHaveAttribute('href', '/settings')
  })
})

function response(body: unknown, status = 200) {
  return { ok: status >= 200 && status < 300, status, json: async () => body } as Response
}
