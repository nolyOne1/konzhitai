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

export type DistributionMode = 'all_compatible' | 'server_group' | 'labels' | 'on_demand'

export interface DistributionRule {
  mode: DistributionMode
  serverGroupId?: string
  labels?: Record<string, string>
}

export interface ParameterDefinition {
  name: string
  type: string
  required: boolean
  description?: string
}

export interface ResourceRequirements {
  cpuMillicores: number
  memoryBytes: number
  diskBytes: number
}

export interface ScriptManifest {
  runtime: string
  entrypoint: string
  category: string
  tags: string[]
  distribution: DistributionRule
  parameterDefinitions: ParameterDefinition[]
  resources: ResourceRequirements
}

export interface ScriptView {
  id: string
  name: string
  description: string
  runtime: string
  category: string
  tags: string[]
  currentVersionId: string
  currentVersion: number
  draftUpdatedAt: string | null
  createdAt: string
  updatedAt: string
}

export interface ScriptDraft {
  scriptId: string
  baseVersionId: string
  content: string
  manifest: ScriptManifest
  updatedAt: string
}

export interface ScriptVersion {
  id: string
  scriptId: string
  number: number
  artifactUri: string
  artifactSha256: string
  entrypoint: string
  manifest: ScriptManifest
  releaseNotes: string
  createdBy: string
  createdAt: string
}

export interface ScriptDetail {
  script: ScriptView
  draft: ScriptDraft
  versions: ScriptVersion[]
}

export type ScriptSyncState = 'pending' | 'downloading' | 'ready' | 'failed' | 'drifted'

export interface ScriptSyncView {
  id: string
  serverId: string
  serverName: string
  scriptId: string
  versionId: string
  versionNumber: number
  state: ScriptSyncState
  artifactSha256: string
  errorCode: string
  errorMessage: string
  blocked: boolean
  syncedAt: string | null
  updatedAt: string
}

export interface CreateScriptInput {
  name: string
  description: string
  runtime: string
  entrypoint?: string
  category?: string
  tags?: string[]
  content?: string
}

export interface ScriptEditorInput {
  content: string
  runtime: string
  entrypoint: string
  category: string
  tags: string[]
  distribution: DistributionRule
  parameterDefinitions: ParameterDefinition[]
  resources: ResourceRequirements
}

export interface PublishScriptInput extends ScriptEditorInput {
  releaseNotes: string
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

export async function getScripts(): Promise<ScriptView[]> {
  const response = await request<{ scripts: ScriptView[] }>('/api/scripts')
  return response.scripts
}

export async function getScript(id: string): Promise<ScriptDetail> {
  return request<ScriptDetail>(`/api/scripts/${encodeURIComponent(id)}`)
}

export async function createScript(input: CreateScriptInput): Promise<ScriptView> {
  return request<ScriptView>('/api/scripts', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
  })
}

export async function importScript(file: File, name = ''): Promise<ScriptView> {
  const body = new FormData()
  body.append('file', file)
  if (name.trim()) body.append('name', name.trim())
  return request<ScriptView>('/api/scripts/import', { method: 'POST', body })
}

export async function saveScriptDraft(id: string, input: ScriptEditorInput): Promise<ScriptDraft> {
  return request<ScriptDraft>(`/api/scripts/${encodeURIComponent(id)}/draft`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
  })
}

export async function publishScript(id: string, input: PublishScriptInput): Promise<ScriptVersion> {
  return request<ScriptVersion>(`/api/scripts/${encodeURIComponent(id)}/publish`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
  })
}

export async function rollbackScript(id: string, versionId: string, releaseNotes: string): Promise<ScriptVersion> {
  return request<ScriptVersion>(`/api/scripts/${encodeURIComponent(id)}/rollback`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ versionId, releaseNotes }),
  })
}

export async function getScriptVersionContent(id: string, versionId: string): Promise<string> {
  const response = await request<{ content: string }>(`/api/scripts/${encodeURIComponent(id)}/versions/${encodeURIComponent(versionId)}/content`)
  return response.content
}

export async function getScriptSyncs(id: string): Promise<ScriptSyncView[]> {
  const response = await request<{ syncs: ScriptSyncView[] }>(`/api/scripts/${encodeURIComponent(id)}/syncs`)
  return response.syncs
}

export async function retryScriptSync(scriptId: string, syncId: string): Promise<void> {
  const response = await fetch(`/api/scripts/${encodeURIComponent(scriptId)}/syncs/${encodeURIComponent(syncId)}/retry`, {
    method: 'POST',
    credentials: 'same-origin',
  })
  if (!response.ok) {
    const failure = await response.json().catch(() => ({ message: '重试同步失败' })) as { message?: string }
    throw new Error(failure.message || '重试同步失败')
  }
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
