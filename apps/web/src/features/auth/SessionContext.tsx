import { createContext, useContext } from 'react'
import type { SessionUser } from '../../api/client'

export const SessionContext = createContext<SessionUser | null>(null)

export function useSession() { return useContext(SessionContext) }

export function useCanExecute() {
  const session = useSession()
  return Boolean(session?.roles.some((role) => role === 'admin' || role === 'operator'))
}
