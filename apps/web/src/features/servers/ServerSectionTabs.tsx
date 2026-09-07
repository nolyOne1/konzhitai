interface ServerSectionTabsProps {
  current: 'nodes' | 'upgrades'
}

export function ServerSectionTabs({ current }: ServerSectionTabsProps) {
  return (
    <nav className="server-section-tabs" aria-label="服务器模块">
      <a className={current === 'nodes' ? 'is-active' : ''} aria-current={current === 'nodes' ? 'page' : undefined} href="/servers">节点管理</a>
      <a className={current === 'upgrades' ? 'is-active' : ''} aria-current={current === 'upgrades' ? 'page' : undefined} href="/servers/upgrades">代理升级</a>
    </nav>
  )
}
