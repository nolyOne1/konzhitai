import './styles.css'
import { useState } from 'react'
import { BrowserRouter, Link, NavLink, Outlet, Route, Routes, useLocation, useNavigate } from 'react-router-dom'

import { logout } from '../api/client'
import { LoginPage } from '../features/auth/LoginPage'
import { DashboardPage } from '../features/dashboard/DashboardPage'
import { ServersPage } from '../features/servers/ServersPage'
import { ScriptEditorPage } from '../features/scripts/ScriptEditorPage'
import { ScriptsPage } from '../features/scripts/ScriptsPage'
import { SyncPage } from '../features/scripts/SyncPage'
import { TaskEditorPage } from '../features/tasks/TaskEditorPage'
import { TasksPage } from '../features/tasks/TasksPage'
import { RunDetailPage } from '../features/runs/RunDetailPage'
import { RunsPage } from '../features/runs/RunsPage'
import { AuditPage } from '../features/settings/AuditPage'
import { MembersPage } from '../features/settings/MembersPage'
import { SecretsPage } from '../features/settings/SecretsPage'
import { OperationsPage } from '../features/operations/OperationsPage'

const navigation = [
  { label: '运行总览', href: '/' },
  { label: '脚本中心', href: '/scripts' },
  { label: '任务调度', href: '/tasks' },
  { label: '执行记录', href: '/runs' },
  { label: '服务器', href: '/servers' },
  { label: '运维中心', href: '/operations' },
  { label: '脚本同步', href: '/sync' },
  { label: '参数与密钥', href: '/secrets' },
  { label: '团队与权限', href: '/members' },
  { label: '系统设置', href: '/settings' },
]

export function App() {
  return (
    <BrowserRouter>
      <Routes>
        <Route path="/login" element={<LoginPage />} />
        <Route element={<ConsoleShell />}>
          <Route index element={<DashboardPage />} />
          <Route path="/scripts" element={<ScriptsPage />} />
          <Route path="/scripts/:id" element={<ScriptEditorPage />} />
          <Route path="/tasks" element={<TasksPage />} />
          <Route path="/tasks/new" element={<TaskEditorPage />} />
          <Route path="/tasks/:id" element={<TaskEditorPage />} />
          <Route path="/runs" element={<RunsPage />} />
          <Route path="/runs/:id" element={<RunDetailPage />} />
          <Route path="/servers" element={<ServersPage />} />
          <Route path="/operations" element={<OperationsPage />} />
          <Route path="/sync" element={<SyncPage />} />
          <Route path="/secrets" element={<SecretsPage />} />
          <Route path="/members" element={<MembersPage />} />
          <Route path="/settings" element={<AuditPage />} />
          <Route path="*" element={<PlaceholderPage />} />
        </Route>
      </Routes>
    </BrowserRouter>
  )
}

function ConsoleShell() {
  const location = useLocation()
  const navigate = useNavigate()
  const [loggingOut, setLoggingOut] = useState(false)
  const [logoutError, setLogoutError] = useState('')
  const current = navigation.find((item) => item.href !== '/' && location.pathname.startsWith(item.href)) ?? navigation[0]

  async function exitSession() {
    if (loggingOut) return
    setLogoutError('')
    setLoggingOut(true)
    try {
      await logout()
      navigate('/login', { replace: true })
    } catch (error) {
      setLogoutError(error instanceof Error ? error.message : '退出登录失败，请稍后重试')
    } finally {
      setLoggingOut(false)
    }
  }

  return (
    <div className="app-shell">
      <a className="skip-link" href="#main-content">跳到主要内容</a>
      <aside className="sidebar">
        <Link className="brand" to="/" aria-label="云令首页">
          <span className="brand-mark" aria-hidden="true">令</span>
          <span className="brand-copy">
            <strong>云令</strong>
            <small>脚本调度中心</small>
          </span>
        </Link>

        <nav className="main-nav" aria-label="主导航">
          {navigation.map((item) => (
            <NavLink
              className={({ isActive }) => `nav-link${isActive ? ' is-active' : ''}`}
              to={item.href}
              key={item.href}
              end={item.href === '/'}
            >
              <span className="nav-marker" aria-hidden="true" />
              <span>{item.label}</span>
            </NavLink>
          ))}
        </nav>

        <div className="sidebar-status">
          <span className="status-dot" aria-hidden="true" />
          调度服务正常
        </div>
      </aside>

      <div className="workspace">
        <header className="topbar">
          <span className="breadcrumb">工作台 / {current.label}</span>
          <div className="topbar-actions">
            <span className="system-health">
              <span className="status-dot" aria-hidden="true" />
              全部系统正常
            </span>
            {logoutError ? <span aria-label="退出登录失败" className="topbar-error" role="alert">{logoutError}</span> : null}
            <button aria-busy={loggingOut} className="secondary-action" disabled={loggingOut} type="button" onClick={() => void exitSession()}>
              {loggingOut ? '正在退出…' : '退出登录'}
            </button>
          </div>
        </header>

        <main className="page-content" id="main-content" tabIndex={-1}><Outlet /></main>
      </div>
    </div>
  )
}

function PlaceholderPage() {
  return (
    <>
      <div className="page-heading"><div><p className="eyebrow">功能建设中</p><h1>控制台模块</h1><p>该模块将在后续阶段按计划接入实际数据。</p></div></div>
      <section className="empty-state" aria-labelledby="setup-title"><span className="empty-state-mark" aria-hidden="true" /><div><h2 id="setup-title">控制台已就绪</h2><p>当前优先完成服务器、脚本和任务调度主链路。</p></div></section>
    </>
  )
}
