export type ServerStatus = 'pending' | 'online' | 'offline' | 'draining' | 'quarantined'

export interface ServerView {
  id: string
  name: string
  cloudProvider: string
  region: string
  status: ServerStatus
  enabled: boolean
  draining: boolean
  labels: Record<string, string>
  runtimes: string[]
  agentVersion: string
  schedulingWeight: number
  cpuUsagePercent: number
  memoryTotalBytes: number
  memoryAvailableBytes: number
  diskTotalBytes: number
  diskAvailableBytes: number
  runningTasks: number
  lastSeenAt: string | null
}

export interface RecentEvent {
  id: string
  type: string
  message: string
  occurredAt: string
}

export interface DashboardData {
  onlineServers: number
  totalServers: number
  runningRuns: number
  queuedRuns: number
  todaySuccessRate: number
  servers: ServerView[]
  recentEvents: RecentEvent[]
}

export interface UpdateServerInput {
  name?: string
  labels?: Record<string, string>
  schedulingWeight?: number
  enabled?: boolean
  draining?: boolean
}

export async function getDashboard(): Promise<DashboardData> {
  return request<DashboardData>('/api/dashboard')
}

export async function getServers(): Promise<ServerView[]> {
  const response = await request<{ servers: ServerView[] }>('/api/servers')
  return response.servers
}

export async function updateServer(id: string, input: UpdateServerInput): Promise<ServerView> {
  return request<ServerView>(`/api/servers/${encodeURIComponent(id)}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
  })
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(path, {
    ...init,
    credentials: 'same-origin',
  })
  if (!response.ok) {
    const failure = await response.json().catch(() => ({ message: '请求失败' })) as { message?: string }
    throw new Error(failure.message || '请求失败')
  }
  try {
    return await response.json() as T
  } catch {
    throw new Error('服务返回的数据格式不正确')
  }
}
