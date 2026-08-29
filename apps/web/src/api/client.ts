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

export type TaskVersionPolicy = 'latest' | 'pinned'

export interface TaskResources {
  cpuMillicores: number
  memoryBytes: number
  diskBytes: number
}

export interface TaskRetryPolicy {
  maxRetries: number
  backoffSeconds: number
}

export interface TaskDefinition {
  id: string
  name: string
  description: string
  scriptId: string
  scriptName: string
  versionPolicy: TaskVersionPolicy
  pinnedVersionId?: string
  parameters: Record<string, unknown>
  secretRefs: Record<string, string>
  priority: number
  requiredLabels: Record<string, string>
  requiredRuntime: string
  resources: TaskResources
  maxConcurrency: number
  timeoutSeconds: number
  maxWaitSeconds: number
  retryPolicy: TaskRetryPolicy
  idempotent: boolean
  enabled: boolean
  createdAt: string
  updatedAt: string
}

export type TaskInput = Omit<TaskDefinition, 'id' | 'scriptName' | 'createdAt' | 'updatedAt'>

export interface TaskRun {
  id: string
  definitionId: string
  scriptVersionId: string
  triggerType: 'manual' | 'schedule' | 'retry'
  state: string
  requiredLabels: Record<string, string>
  requiredRuntime: string
  queuedAt: string
}

export type RunState = 'queued' | 'scheduling' | 'assigned' | 'syncing' | 'running' | 'succeeded' | 'failed' | 'timed_out' | 'cancelled' | 'expired' | 'unknown'

export interface RunView {
  id: string
  definitionId: string
  taskName: string
  scriptId: string
  scriptName: string
  scriptVersionId: string
  versionNumber: number
  serverId?: string
  serverName?: string
  triggerType: 'manual' | 'schedule' | 'retry'
  state: RunState
  parameters: Record<string, unknown>
  resources: TaskResources
  requiredRuntime: string
  priority: number
  attempt: number
  maxRetries: number
  idempotent: boolean
  processConfirmedGone: boolean
  queuedAt: string
  assignedAt?: string
  startedAt?: string
  finishedAt?: string
  exitCode?: number
  resultSummary: string
  createdAt: string
}

export interface TaskSchedule {
  id: string
  definitionId: string
  cronExpression: string
  timezone: string
  enabled: boolean
  nextRunAt?: string
}

export interface TaskScheduleInput {
  cronExpression: string
  timezone: string
  enabled: boolean
}

export type RoleName = 'admin' | 'operator' | 'developer' | 'viewer'

export interface SessionUser {
  id: string
  displayName: string
  email: string
  roles: RoleName[]
}

export interface SecretMetadata {
  id: string
  name: string
  createdBy?: string
  createdAt: string
  updatedAt: string
}

export interface Member {
  id: string
  email: string
  displayName: string
  enabled: boolean
  roles: RoleName[]
  createdAt: string
}

export interface AuditEvent {
  id: string
  actorId?: string
  action: string
  targetType: string
  targetId: string
  details: Record<string, unknown>
  ipAddress?: string
  createdAt: string
}

export interface SystemAlert {
  id: string
  resourceType: string
  resourceId: string
  code: string
  severity: 'info' | 'warning' | 'critical'
  title: string
  message: string
  status: 'open' | 'acknowledged' | 'resolved'
  occurrences: number
  firstOccurredAt: string
  lastOccurredAt: string
  acknowledgedBy?: string
  acknowledgedAt?: string
}

export interface AgentCredentials {
  server_id: string
  credential: string
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

export async function getTasks(): Promise<TaskDefinition[]> {
  const response = await request<{ tasks: TaskDefinition[] }>('/api/tasks')
  return response.tasks
}

export async function getTask(id: string): Promise<TaskDefinition> {
  return request<TaskDefinition>(`/api/tasks/${encodeURIComponent(id)}`)
}

export async function createTask(input: TaskInput): Promise<TaskDefinition> {
  return request<TaskDefinition>('/api/tasks', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
  })
}

export async function updateTask(id: string, input: TaskInput): Promise<TaskDefinition> {
  return request<TaskDefinition>(`/api/tasks/${encodeURIComponent(id)}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
  })
}

export async function setTaskEnabled(id: string, enabled: boolean, cancelQueued = false): Promise<void> {
  await request<void>(`/api/tasks/${encodeURIComponent(id)}/enabled`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ enabled, cancelQueued }),
  })
}

export async function runTask(id: string, parameters: Record<string, unknown> = {}): Promise<TaskRun> {
  return request<TaskRun>(`/api/tasks/${encodeURIComponent(id)}/run`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ parameters }),
  })
}

export async function getRuns(): Promise<RunView[]> {
  const response = await request<{ runs: RunView[] }>('/api/runs')
  return response.runs
}

export async function getRun(id: string): Promise<RunView> {
  return request<RunView>(`/api/runs/${encodeURIComponent(id)}`)
}

export async function cancelRun(id: string): Promise<void> {
  await request<void>(`/api/runs/${encodeURIComponent(id)}/cancel`, { method: 'POST' })
}

export async function retryRun(id: string): Promise<string> {
  const response = await request<{ id: string }>(`/api/runs/${encodeURIComponent(id)}/retry`, { method: 'POST' })
  return response.id
}

export async function getTaskSchedules(id: string): Promise<TaskSchedule[]> {
  const response = await request<{ schedules: TaskSchedule[] }>(`/api/tasks/${encodeURIComponent(id)}/schedules`)
  return response.schedules
}

export async function createTaskSchedule(id: string, input: TaskScheduleInput): Promise<TaskSchedule> {
  return request<TaskSchedule>(`/api/tasks/${encodeURIComponent(id)}/schedules`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
  })
}

export async function validateTaskCron(input: Pick<TaskScheduleInput, 'cronExpression' | 'timezone'>): Promise<void> {
  await request<{ valid: boolean }>('/api/tasks/cron/validate', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
  })
}

export async function getSession(): Promise<SessionUser> {
  const response = await request<{ user: { user_id: string; display_name: string; email: string; roles: RoleName[] } }>('/api/auth/session')
  return { id: response.user.user_id, displayName: response.user.display_name, email: response.user.email, roles: response.user.roles }
}

export async function getSecrets(): Promise<SecretMetadata[]> {
  const response = await request<{ secrets: SecretMetadata[] }>('/api/secrets')
  return response.secrets
}

export async function createSecret(name: string, value: string): Promise<SecretMetadata> {
  return request<SecretMetadata>('/api/secrets', {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name, value }),
  })
}

export async function getMembers(): Promise<Member[]> {
  const response = await request<{ members: Member[] }>('/api/members')
  return response.members
}

export async function updateMemberRoles(id: string, roles: RoleName[]): Promise<Member> {
  return request<Member>(`/api/members/${encodeURIComponent(id)}/roles`, {
    method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ roles }),
  })
}

export async function getAuditEvents(): Promise<AuditEvent[]> {
  const response = await request<{ events: AuditEvent[] }>('/api/audit')
  return response.events
}

export async function getAlerts(): Promise<SystemAlert[]> {
  const response = await request<{ alerts: SystemAlert[] }>('/api/alerts')
  return response.alerts
}

export async function acknowledgeAlert(id: string): Promise<void> {
  await request<void>(`/api/alerts/${encodeURIComponent(id)}/acknowledge`, { method: 'POST' })
}

export async function rotateServerCredential(id: string): Promise<AgentCredentials> {
  return request<AgentCredentials>(`/api/servers/${encodeURIComponent(id)}/credentials/rotate`, { method: 'POST' })
}

export async function revokeServerCredentials(id: string): Promise<void> {
  await request<void>(`/api/servers/${encodeURIComponent(id)}/credentials/revoke`, { method: 'POST' })
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
  if (response.status === 204) return undefined as T
  try {
    return await response.json() as T
  } catch {
    throw new Error('服务返回的数据格式不正确')
  }
}
