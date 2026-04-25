import {
  IconDashboard, IconSessions, IconShield, IconMic, IconClock,
  IconSettings, IconLogo, IconRefresh, IconTool,
} from './shared/Icons'

export function TopNav({ activePage, onNavigate, activeCount, status, lastUpdated, isRefreshing }) {
  const navItems = [
    { id: 'dashboard', label: 'Dashboard', icon: IconDashboard },
    { id: 'sessions', label: 'Sessions', icon: IconSessions },
    { id: 'tooluse', label: 'Tool Use', icon: IconTool },
    { id: 'flagged', label: 'Flagged', icon: IconShield },
    { id: 'voice', label: 'Voice', icon: IconMic },
    { id: 'history', label: 'History', icon: IconClock },
    { id: 'settings', label: 'Settings', icon: IconSettings },
  ]

  return (
    <nav class="topnav">
      <div class="topnav-logo" onClick={() => onNavigate('dashboard')}>
        <IconLogo />
        <span class="topnav-logo-text">ELIDA</span>
      </div>

      <div class="topnav-items">
        {navItems.map((item) => (
          <button
            key={item.id}
            class={'topnav-item' + (activePage === item.id ? ' active' : '')}
            onClick={() => onNavigate(item.id)}
          >
            <item.icon />
            <span>{item.label}</span>
          </button>
        ))}
      </div>

      <div class="topnav-right">
        <div class={'refresh-indicator' + (isRefreshing ? ' refreshing' : '')}>
          <IconRefresh />
          <span>{lastUpdated ? `${lastUpdated}` : 'Connecting...'}</span>
        </div>
        <div class="status-indicator">
          <div class={'status-dot ' + status}></div>
        </div>
        {activeCount > 0 && (
          <span class="topnav-active-badge">{activeCount} active</span>
        )}
      </div>
    </nav>
  )
}
