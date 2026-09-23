import type { ReactNode } from 'react'
import type { RoleName } from '../api/client'
import { SessionContext } from '../features/auth/SessionContext'

export function withSession(children: ReactNode, roles: RoleName[] = ['admin']) {
  return <SessionContext.Provider value={{ id: 'test-user', displayName: '测试用户', email: 'test@example.com', roles, mustChangePassword: false }}>{children}</SessionContext.Provider>
}
