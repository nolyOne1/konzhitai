import type { RunState } from './client'

export interface RunStreamEvent {
  id: string
  kind: 'state' | 'log'
  state?: RunState
  eventType?: string
  stream?: 'stdout' | 'stderr' | 'system'
  sequence: number
  message?: string
  content?: string
  exitCode?: number
  occurredAt: string
}

export function subscribeRunEvents(
  runId: string,
  onEvent: (event: RunStreamEvent) => void,
  onError: () => void,
) {
  const source = new EventSource(`/api/runs/${encodeURIComponent(runId)}/events`)
  const receive = (raw: Event) => {
    try {
      onEvent(JSON.parse((raw as MessageEvent<string>).data) as RunStreamEvent)
    } catch {
      onError()
    }
  }
  source.addEventListener('state', receive)
  source.addEventListener('log', receive)
  source.onerror = onError
  return () => source.close()
}
