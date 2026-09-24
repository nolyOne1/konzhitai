import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, it, vi } from 'vitest'

import { ServersPage } from './ServersPage'

afterEach(() => vi.unstubAllGlobals())

it('创建分组、节点加入和退出分组，改名保持组标识', async () => {
  let groups: Array<{ id: string; name: string; serverCount: number; createdAt: string }> = []
  let server = { id: 'node-1', name: '执行节点', cloudProvider: '京东云', region: '华北', status: 'online', enabled: true, draining: false, labels: {}, runtimes: ['bash'], agentVersion: '0.2.5', agentCapabilities: [], schedulingWeight: 100, cpuUsagePercent: 10, memoryTotalBytes: 4096, memoryAvailableBytes: 2048, runningTasks: 0, serverGroupId: '' }
  const requests: unknown[] = []
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input)
    let body: unknown = {}
    if (path === '/api/auth/session') body = { user: { user_id: 'admin-1', roles: ['admin'] } }
    else if (path === '/api/agent-releases') body = { releases: [] }
    else if (path === '/api/server-groups' && init?.method === 'POST') { groups = [{ id: 'group-1', name: JSON.parse(String(init.body)).name, serverCount: 0, createdAt: '2026-09-23T00:00:00Z' }]; body = groups[0] }
    else if (path === '/api/server-groups/group-1') { groups[0].name = JSON.parse(String(init?.body)).name; body = groups[0] }
    else if (path === '/api/server-groups') body = { groups: groups.map((group) => ({ ...group, serverCount: server.serverGroupId === group.id ? 1 : 0 })) }
    else if (path === '/api/servers/node-1') { const update = JSON.parse(String(init?.body)); requests.push(update); server = { ...server, ...update }; body = server }
    else if (path === '/api/servers') body = { servers: [server] }
    else throw new Error(`unexpected path ${path}`)
    return { ok: true, status: 200, json: async () => body } as Response
  }))
  const user = userEvent.setup()
  render(<ServersPage />)
  await user.type(await screen.findByLabelText('分组名称'), '夜间作业')
  await user.click(screen.getByRole('button', { name: '创建分组' }))
  expect(await screen.findByRole('status')).toHaveTextContent('已创建“夜间作业”')
  await user.click(screen.getByRole('button', { name: '查看执行节点详情' }))
  await user.selectOptions(screen.getByLabelText('所属服务器组'), 'group-1')
  await user.click(screen.getByRole('button', { name: '保存更改' }))
  await waitFor(() => expect(requests).toContainEqual(expect.objectContaining({ serverGroupId: 'group-1' })))
  await user.selectOptions(screen.getByLabelText('所属服务器组'), '')
  await user.click(screen.getByRole('button', { name: '保存更改' }))
  await waitFor(() => expect(requests).toContainEqual(expect.objectContaining({ serverGroupId: '' })))
  await user.click(screen.getByRole('button', { name: '关闭服务器详情' }))
  await user.click(screen.getByRole('button', { name: '更名分组夜间作业' }))
  await user.clear(screen.getByLabelText('新的分组名称'))
  await user.type(screen.getByLabelText('新的分组名称'), '生产批处理')
  await user.click(screen.getByRole('button', { name: '保存分组名称' }))
  expect(await screen.findByRole('status')).toHaveTextContent('已有发布规则保持有效')
  expect(groups[0].id).toBe('group-1')
})
