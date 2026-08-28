import './styles.css'

const navigation = [
  { label: '运行总览', href: '/' },
  { label: '脚本中心', href: '/scripts' },
  { label: '任务调度', href: '/tasks' },
  { label: '执行记录', href: '/runs' },
  { label: '服务器', href: '/servers' },
  { label: '脚本同步', href: '/sync' },
  { label: '参数与密钥', href: '/secrets' },
  { label: '团队与权限', href: '/members' },
  { label: '系统设置', href: '/settings' },
]

export function App() {
  return (
    <div className="app-shell">
      <aside className="sidebar">
        <a className="brand" href="/" aria-label="云令首页">
          <span className="brand-mark" aria-hidden="true">令</span>
          <span className="brand-copy">
            <strong>云令</strong>
            <small>脚本调度中心</small>
          </span>
        </a>

        <nav className="main-nav" aria-label="主导航">
          {navigation.map((item, index) => (
            <a
              className={index === 0 ? 'nav-link is-active' : 'nav-link'}
              href={item.href}
              key={item.href}
              aria-current={index === 0 ? 'page' : undefined}
            >
              <span className="nav-marker" aria-hidden="true" />
              <span>{item.label}</span>
            </a>
          ))}
        </nav>

        <div className="sidebar-status">
          <span className="status-dot" aria-hidden="true" />
          调度服务正常
        </div>
      </aside>

      <div className="workspace">
        <header className="topbar">
          <span className="breadcrumb">工作台 / 运行总览</span>
          <span className="system-health">
            <span className="status-dot" aria-hidden="true" />
            全部系统正常
          </span>
        </header>

        <main className="page-content">
          <div className="page-heading">
            <div>
              <h1>运行总览</h1>
              <p>跨服务器脚本、任务负载和同步状态集中查看</p>
            </div>
            <button type="button" className="primary-action">新建任务</button>
          </div>

          <section className="empty-state" aria-labelledby="setup-title">
            <span className="empty-state-mark" aria-hidden="true" />
            <div>
              <h2 id="setup-title">控制台已就绪</h2>
              <p>服务器接入后，这里将显示实时资源、运行任务和脚本同步状态。</p>
            </div>
          </section>
        </main>
      </div>
    </div>
  )
}
