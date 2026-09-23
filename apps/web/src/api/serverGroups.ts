import { request } from './client'

export interface ServerGroup {
  id: string
  name: string
  serverCount: number
  createdAt: string
}

export async function getServerGroups(): Promise<ServerGroup[]> {
  const response = await request<{ groups: ServerGroup[] }>('/api/server-groups')
  return response.groups ?? []
}

export function createServerGroup(name: string): Promise<ServerGroup> {
  return request('/api/server-groups', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name }) })
}

export function renameServerGroup(id: string, name: string): Promise<ServerGroup> {
  return request(`/api/server-groups/${encodeURIComponent(id)}`, { method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name }) })
}
