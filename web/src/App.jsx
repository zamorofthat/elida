import { useState, useEffect, useRef } from 'preact/hooks'
import { formatBytes, formatDuration, formatDurationStr, truncateId } from './utils'
import { apiFetch, AUTH_KEY, setLogoutHandler } from './apiFetch'

const API_BASE = ''

// ============================================================================
// Login Component
// ============================================================================

export function Login({ onLogin }) {
  const [key, setKey] = useState('')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)

  const handleSubmit = async (e) => {
    e.preventDefault()
    if (!key.trim()) return
    setLoading(true)
    setError('')
    try {
      const res = await fetch(API_BASE + '/control/health', {
        headers: { 'Authorization': 'Bearer ' + key.trim() },
      })
      if (res.status === 401) {
        setError('Invalid API key')
      } else if (res.ok) {
        localStorage.setItem(AUTH_KEY, key.trim())
        onLogin()
      } else {
        setError('Connection failed')
      }
    } catch {
      setError('Cannot reach server')
    } finally {
      setLoading(false)
    }
  }

  return (
    <div class="login-container">
      <div class="login-card">
        <div class="login-header">
          <IconLogo />
          <h1>ELIDA</h1>
          <p class="text-muted">Enter your API key to continue</p>
        </div>
        <form onSubmit={handleSubmit}>
          <input
            type="password"
            class="login-input"
            placeholder="API key"
            value={key}
            onInput={(e) => setKey(e.target.value)}
            autoFocus
          />
          {error && <div class="login-error">{error}</div>}
          <button type="submit" class="btn btn-primary login-btn" disabled={loading || !key.trim()}>
            {loading ? 'Verifying...' : 'Sign In'}
          </button>
        </form>
      </div>
    </div>
  )
}

// ============================================================================
// Icons (SVG components)
// ============================================================================

const IconDashboard = () => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
    <rect x="3" y="3" width="7" height="7" rx="1" />
    <rect x="14" y="3" width="7" height="7" rx="1" />
    <rect x="3" y="14" width="7" height="7" rx="1" />
    <rect x="14" y="14" width="7" height="7" rx="1" />
  </svg>
)

const IconSessions = () => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
    <path d="M17 21v-2a4 4 0 0 0-4-4H5a4 4 0 0 0-4 4v2" />
    <circle cx="9" cy="7" r="4" />
    <path d="M23 21v-2a4 4 0 0 0-3-3.87" />
    <path d="M16 3.13a4 4 0 0 1 0 7.75" />
  </svg>
)

const IconShield = () => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
    <path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z" />
  </svg>
)

const IconMic = () => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
    <path d="M12 1a3 3 0 0 0-3 3v8a3 3 0 0 0 6 0V4a3 3 0 0 0-3-3z" />
    <path d="M19 10v2a7 7 0 0 1-14 0v-2" />
    <line x1="12" y1="19" x2="12" y2="23" />
    <line x1="8" y1="23" x2="16" y2="23" />
  </svg>
)

const IconClock = () => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
    <circle cx="12" cy="12" r="10" />
    <polyline points="12 6 12 12 16 14" />
  </svg>
)

const IconSearch = () => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
    <circle cx="11" cy="11" r="8" />
    <line x1="21" y1="21" x2="16.65" y2="16.65" />
  </svg>
)

const IconRefresh = () => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
    <polyline points="23 4 23 10 17 10" />
    <path d="M20.49 15a9 9 0 1 1-2.12-9.36L23 10" />
  </svg>
)

const IconX = () => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
    <line x1="18" y1="6" x2="6" y2="18" />
    <line x1="6" y1="6" x2="18" y2="18" />
  </svg>
)

const IconLogo = () => (
  <svg viewBox="0 0 32 32" fill="none">
    <rect width="32" height="32" rx="8" fill="#6366f1" />
    <path d="M8 8h16v3H8zM8 14h12v3H8zM8 20h16v3H8z" fill="white" />
  </svg>
)

const IconEmpty = () => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5">
    <path d="M20 7l-8-4-8 4m16 0l-8 4m8-4v10l-8 4m0-10L4 7m8 4v10M4 7v10l8 4" />
  </svg>
)

const IconSettings = () => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
    <circle cx="12" cy="12" r="3" />
    <path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 0 1 0 2.83 2 2 0 0 1-2.83 0l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-2 2 2 2 0 0 1-2-2v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 0 1-2.83 0 2 2 0 0 1 0-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1-2-2 2 2 0 0 1 2-2h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 0 1 0-2.83 2 2 0 0 1 2.83 0l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 2-2 2 2 0 0 1 2 2v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 0 1 2.83 0 2 2 0 0 1 0 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 2 2 2 2 0 0 1-2 2h-.09a1.65 1.65 0 0 0-1.51 1z" />
  </svg>
)

const IconSave = () => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
    <path d="M19 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11l5 5v11a2 2 0 0 1-2 2z" />
    <polyline points="17 21 17 13 7 13 7 21" />
    <polyline points="7 3 7 8 15 8" />
  </svg>
)

const IconReset = () => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
    <path d="M3 12a9 9 0 1 0 9-9 9.75 9.75 0 0 0-6.74 2.74L3 8" />
    <path d="M3 3v5h5" />
  </svg>
)

// ============================================================================
// Sparkline Component
// ============================================================================

function Sparkline({ data, width = 80, height = 32, color = '#6366f1' }) {
  if (!data || data.length < 2) return null

  const max = Math.max(...data)
  const min = Math.min(...data)
  const range = max - min || 1

  const points = data.map((value, index) => {
    const x = (index / (data.length - 1)) * width
    const y = height - ((value - min) / range) * (height - 4) - 2
    return `${x},${y}`
  }).join(' ')

  return (
    <svg width={width} height={height} class="metric-sparkline">
      <defs>
        <linearGradient id="sparkline-gradient" x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" stop-color={color} stop-opacity="0.3" />
          <stop offset="100%" stop-color={color} stop-opacity="0" />
        </linearGradient>
      </defs>
      <polyline points={points} class="sparkline" style={{ stroke: color }} />
    </svg>
  )
}

// ============================================================================
// Search Input Component
// ============================================================================

function SearchInput({ value, onChange, placeholder = 'Search...' }) {
  return (
    <div class="search-input-wrapper">
      <IconSearch />
      <input
        type="text"
        class="search-input"
        value={value}
        onInput={(e) => onChange(e.target.value)}
        placeholder={placeholder}
      />
    </div>
  )
}

// ============================================================================
// Badge Components
// ============================================================================

function StateBadge({ state }) {
  return <span class={'state-badge state-' + state}>{state}</span>
}

function SeverityBadge({ severity }) {
  return <span class={'severity-badge severity-' + severity}>{severity}</span>
}

function ProtocolBadge({ protocol }) {
  return <span class="protocol-badge">{protocol}</span>
}

// ============================================================================
// Metric Card Component
// ============================================================================

function MetricCard({ label, value, className, sparklineData }) {
  return (
    <div class="metric-card">
      <div class="metric-content">
        <div class="metric-label">{label}</div>
        <div class={'metric-value ' + (className || '')}>{value}</div>
      </div>
      {sparklineData && sparklineData.length > 1 && (
        <Sparkline data={sparklineData} />
      )}
    </div>
  )
}

// ============================================================================
// Sidebar Component
// ============================================================================

function Sidebar({ activePage, onNavigate, flaggedCount }) {
  const navItems = [
    { id: 'dashboard', label: 'Dashboard', icon: IconDashboard },
    { id: 'sessions', label: 'Sessions', icon: IconSessions },
    { id: 'flagged', label: 'Flagged', icon: IconShield, badge: flaggedCount },
    { id: 'voice', label: 'Voice', icon: IconMic },
    { id: 'history', label: 'History', icon: IconClock },
    { id: 'settings', label: 'Settings', icon: IconSettings },
  ]

  return (
    <aside class="sidebar">
      <div class="sidebar-logo">
        <IconLogo />
        <span class="sidebar-logo-text">ELIDA</span>
      </div>
      <nav class="sidebar-nav">
        {navItems.map((item) => (
          <button
            key={item.id}
            class={'nav-item' + (activePage === item.id ? ' active' : '')}
            onClick={() => onNavigate(item.id)}
          >
            <item.icon />
            <span class="nav-item-label">{item.label}</span>
            {item.badge > 0 && (
              <span class="nav-item-badge">{item.badge}</span>
            )}
          </button>
        ))}
      </nav>
    </aside>
  )
}

// ============================================================================
// TopBar Component
// ============================================================================

function TopBar({ title, status, lastUpdated, isRefreshing }) {
  return (
    <header class="topbar">
      <div class="topbar-left">
        <h1 class="topbar-title">{title}</h1>
      </div>
      <div class="topbar-right">
        <div class={'refresh-indicator' + (isRefreshing ? ' refreshing' : '')}>
          <IconRefresh />
          <span>{lastUpdated ? `Updated ${lastUpdated}` : 'Connecting...'}</span>
        </div>
        <div class="status-indicator">
          <div class={'status-dot ' + status}></div>
          <span>{status === 'connected' ? 'Connected' : status === 'disconnected' ? 'Disconnected' : 'Connecting'}</span>
        </div>
      </div>
    </header>
  )
}

// ============================================================================
// Session Table Component
// ============================================================================

function SessionTable({ sessions, showActions, onKill, onViewDetails, searchTerm }) {
  const formatBackends = (session) => {
    if (session.backends_used && Object.keys(session.backends_used).length > 0) {
      const entries = Object.entries(session.backends_used)
      if (entries.length === 1) return entries[0][0]
      return entries.map(([name, count]) => `${name}(${count})`).join(', ')
    }
    try {
      return new URL(session.backend).host
    } catch {
      return session.backend || '-'
    }
  }

  const filtered = searchTerm
    ? sessions.filter(s => {
        const term = searchTerm.toLowerCase()
        return (
          s.id?.toLowerCase().includes(term) ||
          s.client_addr?.toLowerCase().includes(term) ||
          s.state?.toLowerCase().includes(term) ||
          s.backend?.toLowerCase().includes(term) ||
          (s.backends_used && Object.keys(s.backends_used).some(b => b.toLowerCase().includes(term)))
        )
      })
    : sessions

  if (!filtered || filtered.length === 0) {
    return (
      <div class="empty-state">
        <IconEmpty />
        <p>{searchTerm ? 'No matching sessions' : 'No sessions'}</p>
      </div>
    )
  }

  return (
    <div class="table-container">
      <table class="data-table">
        <thead>
          <tr>
            <th>Session ID</th>
            <th>State</th>
            <th>Backends</th>
            <th>Client</th>
            <th>Requests</th>
            <th>Bytes In/Out</th>
            <th>Duration</th>
            {showActions && <th>Action</th>}
          </tr>
        </thead>
        <tbody>
          {filtered.map((s) => (
            <tr
              key={s.id}
              class={onViewDetails ? 'clickable' : ''}
              onClick={() => onViewDetails && onViewDetails(s)}
            >
              <td class="mono">{truncateId(s.id)}</td>
              <td><StateBadge state={s.state} /></td>
              <td class="mono muted">{formatBackends(s)}</td>
              <td class="mono muted">{s.client_addr}</td>
              <td>{s.request_count}</td>
              <td class="mono">{formatBytes(s.bytes_in)} / {formatBytes(s.bytes_out)}</td>
              <td class="mono duration">{s.duration_ms ? formatDuration(s.duration_ms) : formatDurationStr(s.duration)}</td>
              {showActions && (
                <td>
                  <button
                    class="btn btn-danger btn-sm"
                    onClick={(e) => { e.stopPropagation(); onKill(s.id); }}
                    disabled={s.state !== 'active'}
                  >
                    Kill
                  </button>
                </td>
              )}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

// ============================================================================
// Session Details Modal
// ============================================================================

function SessionDetails({ session, onClose }) {
  const [flaggedInfo, setFlaggedInfo] = useState(null)

  useEffect(() => {
    if (!session?.id) return
    const controller = new AbortController()
    const opts = { signal: controller.signal }

    apiFetch(API_BASE + '/control/flagged/' + session.id, opts)
      .then(res => res.ok ? res.json() : null)
      .then(data => {
        if (controller.signal.aborted) return
        if (data) {
          setFlaggedInfo(data)
        } else {
          return apiFetch(API_BASE + '/control/history/' + session.id, opts)
            .then(res => res.ok ? res.json() : null)
            .then(histData => { if (!controller.signal.aborted) setFlaggedInfo(histData) })
        }
      })
      .catch((err) => { if (!controller.signal.aborted) setFlaggedInfo(null) })

    return () => controller.abort()
  }, [session?.id])

  if (!session) return null

  // Merge violations/captured from session object (history inline) and flaggedInfo
  const violations = flaggedInfo?.violations || session.violations || null
  const capturedContent = flaggedInfo?.captured_content || session.captured_content || null

  return (
    <div class="modal-overlay" onClick={onClose}>
      <div class="modal" onClick={(e) => e.stopPropagation()}>
        <div class="modal-header">
          <h2>Session Details</h2>
          <button class="btn btn-icon btn-secondary" onClick={onClose}>
            <IconX />
          </button>
        </div>
        <div class="modal-body">
          <div class="session-metrics-grid">
            <div class="session-metric">
              <span class="session-metric-label">Session ID</span>
              <span class="session-metric-value mono">{truncateId(session.id, 12)}</span>
            </div>
            <div class="session-metric">
              <span class="session-metric-label">State</span>
              <span class="session-metric-value"><StateBadge state={session.state} /></span>
            </div>
            <div class="session-metric">
              <span class="session-metric-label">Duration</span>
              <span class="session-metric-value">{session.duration_ms ? formatDuration(session.duration_ms) : formatDurationStr(session.duration)}</span>
            </div>
            <div class="session-metric">
              <span class="session-metric-label">Requests</span>
              <span class="session-metric-value">{session.request_count}</span>
            </div>
            <div class="session-metric">
              <span class="session-metric-label">Bytes In</span>
              <span class="session-metric-value">{formatBytes(session.bytes_in)}</span>
            </div>
            <div class="session-metric">
              <span class="session-metric-label">Bytes Out</span>
              <span class="session-metric-value">{formatBytes(session.bytes_out)}</span>
            </div>
            <div class="session-metric">
              <span class="session-metric-label">Backend</span>
              <span class="session-metric-value mono">{session.backend || '-'}</span>
            </div>
            <div class="session-metric">
              <span class="session-metric-label">Client</span>
              <span class="session-metric-value mono">{session.client_addr}</span>
            </div>
            {session.start_time && (
              <div class="session-metric">
                <span class="session-metric-label">Started</span>
                <span class="session-metric-value">{new Date(session.start_time).toLocaleString()}</span>
              </div>
            )}
            {session.end_time && (
              <div class="session-metric">
                <span class="session-metric-label">Ended</span>
                <span class="session-metric-value">{new Date(session.end_time).toLocaleString()}</span>
              </div>
            )}
          </div>

          {violations && violations.length > 0 && (
            <>
              <h3>Policy Violations</h3>
              <div class="violations-list">
                {violations.map((v, i) => (
                  <div key={i} class="violation-item">
                    <div class="violation-header">
                      <SeverityBadge severity={v.severity} />
                      <strong>{v.rule_name}</strong>
                    </div>
                    <div class="violation-desc">{v.description}</div>
                    {v.matched_text && (
                      <div class="violation-match">
                        Matched: <code>{v.matched_text}</code>
                      </div>
                    )}
                  </div>
                ))}
              </div>
            </>
          )}

          {capturedContent && capturedContent.length > 0 && (
            <>
              <h3>Captured Requests</h3>
              <div class="captured-list">
                {capturedContent.map((c, i) => (
                  <div key={i} class="captured-item">
                    <div class="captured-header">
                      <span class="mono">{c.method} {c.path}</span>
                      <span class="muted">{new Date(c.timestamp).toLocaleTimeString()}</span>
                    </div>
                    {c.request_body && (
                      <div class="captured-body">
                        <strong>Request:</strong>
                        <pre>{c.request_body}</pre>
                      </div>
                    )}
                    {c.response_body && (
                      <div class="captured-body">
                        <strong>Response:</strong>
                        <pre>{c.response_body}</pre>
                      </div>
                    )}
                  </div>
                ))}
              </div>
            </>
          )}
        </div>
      </div>
    </div>
  )
}

// ============================================================================
// Flagged Table Component
// ============================================================================

function FlaggedTable({ flagged, onViewDetails, searchTerm }) {
  const filtered = searchTerm
    ? flagged.filter(f =>
        f.session_id?.toLowerCase().includes(searchTerm.toLowerCase()) ||
        f.violations?.some(v => v.rule_name?.toLowerCase().includes(searchTerm.toLowerCase()))
      )
    : flagged

  if (!filtered || filtered.length === 0) {
    return (
      <div class="empty-state">
        <IconShield />
        <p>{searchTerm ? 'No matching flagged sessions' : 'No flagged sessions - all sessions within policy'}</p>
      </div>
    )
  }

  return (
    <div class="table-container">
      <table class="data-table">
        <thead>
          <tr>
            <th>Session ID</th>
            <th>Severity</th>
            <th>Violations</th>
            <th>First Flagged</th>
            <th>Last Flagged</th>
            <th>Action</th>
          </tr>
        </thead>
        <tbody>
          {filtered.map((f) => (
            <tr key={f.session_id}>
              <td class="mono">{truncateId(f.session_id)}</td>
              <td><SeverityBadge severity={f.max_severity} /></td>
              <td>
                {f.violations.map((v, i) => (
                  <span key={i} class="violation-tag">{v.rule_name}</span>
                ))}
              </td>
              <td class="mono muted">{new Date(f.first_flagged).toLocaleTimeString()}</td>
              <td class="mono muted">{new Date(f.last_flagged).toLocaleTimeString()}</td>
              <td>
                <button class="btn btn-secondary btn-sm" onClick={() => onViewDetails(f)}>
                  View
                </button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

// ============================================================================
// Flagged Details Modal
// ============================================================================

function FlaggedDetails({ flagged, onClose }) {
  const [sessionInfo, setSessionInfo] = useState(null)

  useEffect(() => {
    if (!flagged?.session_id) return
    const controller = new AbortController()

    apiFetch(API_BASE + '/control/sessions/' + flagged.session_id, { signal: controller.signal })
      .then(res => res.ok ? res.json() : null)
      .then(data => { if (!controller.signal.aborted) setSessionInfo(data) })
      .catch(() => { if (!controller.signal.aborted) setSessionInfo(null) })

    return () => controller.abort()
  }, [flagged?.session_id])

  if (!flagged) return null

  return (
    <div class="modal-overlay" onClick={onClose}>
      <div class="modal" onClick={(e) => e.stopPropagation()}>
        <div class="modal-header">
          <h2>Flagged Session Details</h2>
          <button class="btn btn-icon btn-secondary" onClick={onClose}>
            <IconX />
          </button>
        </div>
        <div class="modal-body">
          <div class="session-metrics-grid">
            <div class="session-metric">
              <span class="session-metric-label">Session ID</span>
              <span class="session-metric-value mono">{truncateId(flagged.session_id, 12)}</span>
            </div>
            <div class="session-metric">
              <span class="session-metric-label">Max Severity</span>
              <span class="session-metric-value"><SeverityBadge severity={flagged.max_severity} /></span>
            </div>
            {sessionInfo && (
              <>
                <div class="session-metric">
                  <span class="session-metric-label">State</span>
                  <span class="session-metric-value"><StateBadge state={sessionInfo.state} /></span>
                </div>
                <div class="session-metric">
                  <span class="session-metric-label">Duration</span>
                  <span class="session-metric-value">{formatDurationStr(sessionInfo.duration)}</span>
                </div>
                <div class="session-metric">
                  <span class="session-metric-label">Requests</span>
                  <span class="session-metric-value">{sessionInfo.request_count}</span>
                </div>
                <div class="session-metric">
                  <span class="session-metric-label">Bytes In</span>
                  <span class="session-metric-value">{formatBytes(sessionInfo.bytes_in)}</span>
                </div>
                <div class="session-metric">
                  <span class="session-metric-label">Bytes Out</span>
                  <span class="session-metric-value">{formatBytes(sessionInfo.bytes_out)}</span>
                </div>
                <div class="session-metric">
                  <span class="session-metric-label">Client</span>
                  <span class="session-metric-value mono">{sessionInfo.client_addr}</span>
                </div>
              </>
            )}
          </div>

          <h3>Violations</h3>
          <div class="violations-list">
            {flagged.violations.map((v, i) => (
              <div key={i} class="violation-item">
                <div class="violation-header">
                  <SeverityBadge severity={v.severity} />
                  <strong>{v.rule_name}</strong>
                </div>
                <div class="violation-desc">{v.description}</div>
                {v.matched_text && (
                  <div class="violation-match">
                    Matched: <code>{v.matched_text}</code>
                  </div>
                )}
                {v.threshold && (
                  <div class="violation-stats">
                    Threshold: {v.threshold} | Actual: {v.actual_value}
                  </div>
                )}
              </div>
            ))}
          </div>

          {flagged.captured_content && flagged.captured_content.length > 0 && (
            <>
              <h3>Captured Requests</h3>
              <div class="captured-list">
                {flagged.captured_content.map((c, i) => (
                  <div key={i} class="captured-item">
                    <div class="captured-header">
                      <span class="mono">{c.method} {c.path}</span>
                      <span class="muted">{new Date(c.timestamp).toLocaleTimeString()}</span>
                    </div>
                    {c.request_body && (
                      <div class="captured-body">
                        <strong>Request:</strong>
                        <pre>{c.request_body}</pre>
                      </div>
                    )}
                    {c.response_body && (
                      <div class="captured-body">
                        <strong>Response:</strong>
                        <pre>{c.response_body}</pre>
                      </div>
                    )}
                  </div>
                ))}
              </div>
            </>
          )}
        </div>
      </div>
    </div>
  )
}

// ============================================================================
// Voice Table Component
// ============================================================================

function VoiceTable({ voiceSessions, onViewDetails, searchTerm }) {
  const filtered = searchTerm
    ? voiceSessions.filter(v =>
        v.id?.toLowerCase().includes(searchTerm.toLowerCase()) ||
        v.parent_session_id?.toLowerCase().includes(searchTerm.toLowerCase()) ||
        v.protocol?.toLowerCase().includes(searchTerm.toLowerCase())
      )
    : voiceSessions

  if (!filtered || filtered.length === 0) {
    return (
      <div class="empty-state">
        <IconMic />
        <p>{searchTerm ? 'No matching voice sessions' : 'No voice sessions'}</p>
      </div>
    )
  }

  return (
    <div class="table-container">
      <table class="data-table">
        <thead>
          <tr>
            <th>Voice ID</th>
            <th>Parent Session</th>
            <th>State</th>
            <th>Protocol</th>
            <th>Turns</th>
            <th>Duration</th>
            <th>Model</th>
            <th>Action</th>
          </tr>
        </thead>
        <tbody>
          {filtered.map((v) => (
            <tr key={v.id} class="clickable" onClick={() => onViewDetails(v)}>
              <td class="mono">{truncateId(v.id)}</td>
              <td class="mono muted">{truncateId(v.parent_session_id)}</td>
              <td><StateBadge state={v.state} /></td>
              <td><ProtocolBadge protocol={v.protocol || 'unknown'} /></td>
              <td>{v.turn_count || 0}</td>
              <td class="mono duration">{formatDuration(v.duration_ms || v.audio_duration_ms)}</td>
              <td class="mono muted">{v.model || '-'}</td>
              <td>
                <button
                  class="btn btn-secondary btn-sm"
                  onClick={(e) => { e.stopPropagation(); onViewDetails(v); }}
                >
                  View
                </button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

// ============================================================================
// Voice Details Modal
// ============================================================================

function VoiceDetails({ voiceSession, onClose }) {
  const [fullSession, setFullSession] = useState(null)

  useEffect(() => {
    if (voiceSession?.id && voiceSession?.parent_session_id) {
      const controller = new AbortController()

      apiFetch(API_BASE + '/control/voice/' + voiceSession.parent_session_id + '/' + voiceSession.id, { signal: controller.signal })
        .then(res => res.ok ? res.json() : null)
        .then(data => { if (!controller.signal.aborted) setFullSession(data || voiceSession) })
        .catch(() => { if (!controller.signal.aborted) setFullSession(voiceSession) })

      return () => controller.abort()
    } else if (voiceSession) {
      setFullSession(voiceSession)
    }
  }, [voiceSession?.id])

  if (!voiceSession) return null

  const session = fullSession || voiceSession

  return (
    <div class="modal-overlay" onClick={onClose}>
      <div class="modal" onClick={(e) => e.stopPropagation()}>
        <div class="modal-header">
          <h2>Voice Session Details</h2>
          <button class="btn btn-icon btn-secondary" onClick={onClose}>
            <IconX />
          </button>
        </div>
        <div class="modal-body">
          <div class="voice-info">
            <div class="voice-info-item">
              <span class="voice-info-label">Voice ID</span>
              <span class="voice-info-value mono">{session.id}</span>
            </div>
            <div class="voice-info-item">
              <span class="voice-info-label">State</span>
              <span class="voice-info-value"><StateBadge state={session.state} /></span>
            </div>
            <div class="voice-info-item">
              <span class="voice-info-label">Protocol</span>
              <span class="voice-info-value"><ProtocolBadge protocol={session.protocol || 'unknown'} /></span>
            </div>
          </div>

          <div class="session-metrics-grid">
            <div class="session-metric">
              <span class="session-metric-label">Parent Session</span>
              <span class="session-metric-value mono">{truncateId(session.parent_session_id, 12)}</span>
            </div>
            <div class="session-metric">
              <span class="session-metric-label">Turn Count</span>
              <span class="session-metric-value">{session.turn_count || 0}</span>
            </div>
            <div class="session-metric">
              <span class="session-metric-label">Duration</span>
              <span class="session-metric-value">{formatDuration(session.duration_ms || session.audio_duration_ms)}</span>
            </div>
            <div class="session-metric">
              <span class="session-metric-label">Model</span>
              <span class="session-metric-value mono">{session.model || '-'}</span>
            </div>
            {session.voice && (
              <div class="session-metric">
                <span class="session-metric-label">Voice</span>
                <span class="session-metric-value">{session.voice}</span>
              </div>
            )}
            {session.start_time && (
              <div class="session-metric">
                <span class="session-metric-label">Started</span>
                <span class="session-metric-value">{new Date(session.start_time).toLocaleString()}</span>
              </div>
            )}
          </div>

          {session.transcript && session.transcript.length > 0 && (
            <>
              <h3>Transcript</h3>
              <div class="transcript-container">
                {session.transcript.map((entry, i) => (
                  <div key={i} class="transcript-entry">
                    <span class={'transcript-speaker ' + (entry.speaker || 'user')}>
                      {entry.speaker || 'user'}
                    </span>
                    <span class="transcript-text">{entry.text}</span>
                    <span class="transcript-meta">
                      {entry.source && <span>{entry.source}</span>}
                      {entry.timestamp && (
                        <span> {new Date(entry.timestamp).toLocaleTimeString()}</span>
                      )}
                    </span>
                  </div>
                ))}
              </div>
            </>
          )}
        </div>
      </div>
    </div>
  )
}

// ============================================================================
// Settings Page
// ============================================================================

function SettingsPage() {
  const [settings, setSettings] = useState(null)
  const [defaults, setDefaults] = useState(null)
  const [diff, setDiff] = useState({})
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [message, setMessage] = useState(null)

  const fetchSettings = async () => {
    try {
      const [settingsRes, defaultsRes, diffRes] = await Promise.all([
        apiFetch(API_BASE + '/control/settings'),
        apiFetch(API_BASE + '/control/settings/defaults'),
        apiFetch(API_BASE + '/control/settings/diff'),
      ])

      if (settingsRes.ok) setSettings(await settingsRes.json())
      if (defaultsRes.ok) setDefaults(await defaultsRes.json())
      if (diffRes.ok) setDiff(await diffRes.json())
    } catch (err) {
      console.error('Failed to fetch settings:', err)
      setMessage({ type: 'error', text: 'Failed to load settings' })
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    fetchSettings()
  }, [])

  const handleSave = async () => {
    setSaving(true)
    setMessage(null)
    try {
      const res = await apiFetch(API_BASE + '/control/settings', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(settings),
      })
      if (res.ok) {
        setMessage({ type: 'success', text: 'Settings applied instantly (no restart required)' })
        fetchSettings() // Refresh diff
      } else {
        const err = await res.text()
        setMessage({ type: 'error', text: 'Failed to save: ' + err })
      }
    } catch (err) {
      setMessage({ type: 'error', text: 'Failed to save settings' })
    } finally {
      setSaving(false)
    }
  }

  const handleReset = async () => {
    if (!confirm('Reset all settings to defaults? This will delete configs/settings.yaml')) return
    setSaving(true)
    setMessage(null)
    try {
      const res = await apiFetch(API_BASE + '/control/settings', { method: 'DELETE' })
      if (res.ok) {
        setMessage({ type: 'success', text: 'Settings reset to defaults' })
        fetchSettings()
      } else {
        setMessage({ type: 'error', text: 'Failed to reset settings' })
      }
    } catch (err) {
      setMessage({ type: 'error', text: 'Failed to reset settings' })
    } finally {
      setSaving(false)
    }
  }

  const updateSetting = (section, key, value) => {
    setSettings(prev => ({
      ...prev,
      [section]: {
        ...prev[section],
        [key]: value,
      },
    }))
  }

  const updateNestedSetting = (section, subsection, key, value) => {
    setSettings(prev => ({
      ...prev,
      [section]: {
        ...prev[section],
        [subsection]: {
          ...prev[section]?.[subsection],
          [key]: value,
        },
      },
    }))
  }

  const addCustomRule = () => {
    const newRule = {
      name: 'new_rule_' + Date.now(),
      type: 'content_match',
      target: 'both',
      patterns: [],
      severity: 'warning',
      action: 'flag',
      description: '',
    }
    setSettings(prev => ({
      ...prev,
      policy: {
        ...prev.policy,
        custom_rules: [...(prev.policy?.custom_rules || []), newRule],
      },
    }))
  }

  const updateCustomRule = (index, field, value) => {
    setSettings(prev => {
      const rules = [...(prev.policy?.custom_rules || [])]
      rules[index] = { ...rules[index], [field]: value }
      return {
        ...prev,
        policy: { ...prev.policy, custom_rules: rules },
      }
    })
  }

  const removeCustomRule = (index) => {
    setSettings(prev => {
      const rules = [...(prev.policy?.custom_rules || [])]
      rules.splice(index, 1)
      return {
        ...prev,
        policy: { ...prev.policy, custom_rules: rules },
      }
    })
  }

  const isModified = (path) => diff && diff[path]

  if (loading) {
    return (
      <div class="panel">
        <div class="panel-body">
          <p>Loading settings...</p>
        </div>
      </div>
    )
  }

  if (!settings) {
    return (
      <div class="panel">
        <div class="panel-body">
          <p class="error">Settings not available. Is the settings store initialized?</p>
        </div>
      </div>
    )
  }

  const diffCount = Object.keys(diff || {}).length

  return (
    <>
      {message && (
        <div class={'settings-message ' + message.type}>
          {message.text}
        </div>
      )}

      <div class="settings-actions">
        <button class="btn btn-primary" onClick={handleSave} disabled={saving}>
          <IconSave />
          <span>{saving ? 'Saving...' : 'Save Settings'}</span>
        </button>
        <button class="btn btn-secondary" onClick={handleReset} disabled={saving}>
          <IconReset />
          <span>Reset to Defaults</span>
        </button>
        {diffCount > 0 && (
          <span class="settings-diff-count">{diffCount} setting{diffCount !== 1 ? 's' : ''} modified</span>
        )}
      </div>

      <div class="panel">
        <div class="panel-header">
          <h2 class="panel-title">Policy Settings</h2>
        </div>
        <div class="panel-body">
          <div class="settings-grid">
            <div class={'settings-item' + (isModified('policy.enabled') ? ' modified' : '')}>
              <label class="settings-label">
                <span>Policy Enabled</span>
                {isModified('policy.enabled') && <span class="modified-badge">modified</span>}
              </label>
              <select
                class="settings-select"
                value={settings.policy?.enabled ? 'true' : 'false'}
                onChange={(e) => updateSetting('policy', 'enabled', e.target.value === 'true')}
              >
                <option value="true">Enabled</option>
                <option value="false">Disabled</option>
              </select>
            </div>

            <div class={'settings-item' + (isModified('policy.mode') ? ' modified' : '')}>
              <label class="settings-label">
                <span>Policy Mode</span>
                {isModified('policy.mode') && <span class="modified-badge">modified</span>}
              </label>
              <select
                class="settings-select"
                value={settings.policy?.mode || 'enforce'}
                onChange={(e) => updateSetting('policy', 'mode', e.target.value)}
              >
                <option value="enforce">Enforce (block violations)</option>
                <option value="audit">Audit (log only)</option>
              </select>
            </div>

            <div class={'settings-item' + (isModified('policy.preset') ? ' modified' : '')}>
              <label class="settings-label">
                <span>Policy Preset</span>
                {isModified('policy.preset') && <span class="modified-badge">modified</span>}
              </label>
              <select
                class="settings-select"
                value={settings.policy?.preset || 'standard'}
                onChange={(e) => updateSetting('policy', 'preset', e.target.value)}
              >
                <option value="minimal">Minimal (rate limits only)</option>
                <option value="standard">Standard (OWASP basics)</option>
                <option value="strict">Strict (full OWASP + PII)</option>
              </select>
            </div>
          </div>
        </div>
      </div>

      <div class="panel">
        <div class="panel-header">
          <h2 class="panel-title">Risk Ladder Thresholds</h2>
        </div>
        <div class="panel-body">
          <div class="settings-grid">
            <div class={'settings-item' + (isModified('policy.risk_ladder.warn_score') ? ' modified' : '')}>
              <label class="settings-label">
                <span>Warn Score</span>
                {isModified('policy.risk_ladder.warn_score') && <span class="modified-badge">modified</span>}
              </label>
              <input
                type="number"
                class="settings-input"
                value={settings.policy?.risk_ladder?.warn_score ?? 5}
                onChange={(e) => updateNestedSetting('policy', 'risk_ladder', 'warn_score', parseInt(e.target.value))}
              />
            </div>

            <div class={'settings-item' + (isModified('policy.risk_ladder.throttle_score') ? ' modified' : '')}>
              <label class="settings-label">
                <span>Throttle Score</span>
                {isModified('policy.risk_ladder.throttle_score') && <span class="modified-badge">modified</span>}
              </label>
              <input
                type="number"
                class="settings-input"
                value={settings.policy?.risk_ladder?.throttle_score ?? 15}
                onChange={(e) => updateNestedSetting('policy', 'risk_ladder', 'throttle_score', parseInt(e.target.value))}
              />
            </div>

            <div class={'settings-item' + (isModified('policy.risk_ladder.block_score') ? ' modified' : '')}>
              <label class="settings-label">
                <span>Block Score</span>
                {isModified('policy.risk_ladder.block_score') && <span class="modified-badge">modified</span>}
              </label>
              <input
                type="number"
                class="settings-input"
                value={settings.policy?.risk_ladder?.block_score ?? 30}
                onChange={(e) => updateNestedSetting('policy', 'risk_ladder', 'block_score', parseInt(e.target.value))}
              />
            </div>

            <div class={'settings-item' + (isModified('policy.risk_ladder.terminate_score') ? ' modified' : '')}>
              <label class="settings-label">
                <span>Terminate Score</span>
                {isModified('policy.risk_ladder.terminate_score') && <span class="modified-badge">modified</span>}
              </label>
              <input
                type="number"
                class="settings-input"
                value={settings.policy?.risk_ladder?.terminate_score ?? 50}
                onChange={(e) => updateNestedSetting('policy', 'risk_ladder', 'terminate_score', parseInt(e.target.value))}
              />
            </div>
          </div>
        </div>
      </div>

      <div class="panel">
        <div class="panel-header">
          <h2 class="panel-title">Capture Settings</h2>
        </div>
        <div class="panel-body">
          <div class="settings-grid">
            <div class={'settings-item' + (isModified('capture.mode') ? ' modified' : '')}>
              <label class="settings-label">
                <span>Capture Mode</span>
                {isModified('capture.mode') && <span class="modified-badge">modified</span>}
              </label>
              <select
                class="settings-select"
                value={settings.capture?.mode || 'flagged_only'}
                onChange={(e) => updateSetting('capture', 'mode', e.target.value)}
              >
                <option value="flagged_only">Flagged Only (policy violations)</option>
                <option value="all">All (CDR-style full audit)</option>
              </select>
            </div>

            <div class={'settings-item' + (isModified('capture.max_capture_size') ? ' modified' : '')}>
              <label class="settings-label">
                <span>Max Capture Size (bytes)</span>
                {isModified('capture.max_capture_size') && <span class="modified-badge">modified</span>}
              </label>
              <input
                type="number"
                class="settings-input"
                value={settings.capture?.max_capture_size ?? 10000}
                onChange={(e) => updateSetting('capture', 'max_capture_size', parseInt(e.target.value))}
              />
            </div>

            <div class={'settings-item' + (isModified('capture.max_per_session') ? ' modified' : '')}>
              <label class="settings-label">
                <span>Max Per Session</span>
                {isModified('capture.max_per_session') && <span class="modified-badge">modified</span>}
              </label>
              <input
                type="number"
                class="settings-input"
                value={settings.capture?.max_per_session ?? 100}
                onChange={(e) => updateSetting('capture', 'max_per_session', parseInt(e.target.value))}
              />
            </div>
          </div>
        </div>
      </div>

      <div class="panel">
        <div class="panel-header">
          <h2 class="panel-title">Failover Settings</h2>
        </div>
        <div class="panel-body">
          <div class="settings-grid">
            <div class={'settings-item' + (isModified('failover.enabled') ? ' modified' : '')}>
              <label class="settings-label">
                <span>Failover Enabled</span>
                {isModified('failover.enabled') && <span class="modified-badge">modified</span>}
              </label>
              <select
                class="settings-select"
                value={settings.failover?.enabled ? 'true' : 'false'}
                onChange={(e) => updateSetting('failover', 'enabled', e.target.value === 'true')}
              >
                <option value="true">Enabled</option>
                <option value="false">Disabled</option>
              </select>
            </div>

            <div class={'settings-item' + (isModified('failover.max_retries') ? ' modified' : '')}>
              <label class="settings-label">
                <span>Max Retries</span>
                {isModified('failover.max_retries') && <span class="modified-badge">modified</span>}
              </label>
              <input
                type="number"
                class="settings-input"
                value={settings.failover?.max_retries ?? 2}
                onChange={(e) => updateSetting('failover', 'max_retries', parseInt(e.target.value))}
              />
            </div>

            <div class={'settings-item' + (isModified('failover.auto_select') ? ' modified' : '')}>
              <label class="settings-label">
                <span>Auto-Select Best Backend</span>
                {isModified('failover.auto_select') && <span class="modified-badge">modified</span>}
              </label>
              <select
                class="settings-select"
                value={settings.failover?.auto_select ? 'true' : 'false'}
                onChange={(e) => updateSetting('failover', 'auto_select', e.target.value === 'true')}
              >
                <option value="true">Enabled</option>
                <option value="false">Disabled</option>
              </select>
            </div>
          </div>
        </div>
      </div>

      <div class="panel">
        <div class="panel-header">
          <h2 class="panel-title">Custom Rules</h2>
          <button class="btn btn-primary btn-sm" onClick={addCustomRule}>
            + Add Rule
          </button>
        </div>
        <div class="panel-body">
          <p class="settings-hint">
            Custom rules are <strong>appended</strong> to the preset rules.
            They run in addition to the built-in rules, not instead of them.
            Patterns use <a href="https://github.com/google/re2/wiki/Syntax" target="_blank" rel="noopener">RE2 regex syntax</a> (case-insensitive).
          </p>
          {(!settings.policy?.custom_rules || settings.policy.custom_rules.length === 0) ? (
            <p class="muted">No custom rules defined. Click "Add Rule" to create one.</p>
          ) : (
            <div class="custom-rules-list">
              {settings.policy.custom_rules.map((rule, index) => (
                <div key={index} class="custom-rule-card">
                  <div class="custom-rule-header">
                    <input
                      type="text"
                      class="settings-input rule-name-input"
                      value={rule.name}
                      onChange={(e) => updateCustomRule(index, 'name', e.target.value)}
                      placeholder="Rule name"
                    />
                    <button
                      class="btn btn-danger btn-sm"
                      onClick={() => removeCustomRule(index)}
                    >
                      Remove
                    </button>
                  </div>
                  <div class="custom-rule-grid">
                    <div class="settings-item">
                      <label class="settings-label">Type</label>
                      <select
                        class="settings-select"
                        value={rule.type}
                        onChange={(e) => updateCustomRule(index, 'type', e.target.value)}
                      >
                        <option value="content_match">Content Match (regex)</option>
                        <option value="bytes_out">Bytes Out (threshold)</option>
                        <option value="bytes_in">Bytes In (threshold)</option>
                        <option value="request_count">Request Count</option>
                        <option value="duration">Duration (seconds)</option>
                        <option value="requests_per_minute">Requests/Minute</option>
                      </select>
                    </div>
                    <div class="settings-item">
                      <label class="settings-label">Target</label>
                      <select
                        class="settings-select"
                        value={rule.target || 'both'}
                        onChange={(e) => updateCustomRule(index, 'target', e.target.value)}
                      >
                        <option value="both">Both</option>
                        <option value="request">Request Only</option>
                        <option value="response">Response Only</option>
                      </select>
                    </div>
                    <div class="settings-item">
                      <label class="settings-label">Severity</label>
                      <select
                        class="settings-select"
                        value={rule.severity}
                        onChange={(e) => updateCustomRule(index, 'severity', e.target.value)}
                      >
                        <option value="info">Info</option>
                        <option value="warning">Warning</option>
                        <option value="critical">Critical</option>
                      </select>
                    </div>
                    <div class="settings-item">
                      <label class="settings-label">Action</label>
                      <select
                        class="settings-select"
                        value={rule.action}
                        onChange={(e) => updateCustomRule(index, 'action', e.target.value)}
                      >
                        <option value="flag">Flag (log only)</option>
                        <option value="block">Block (reject request)</option>
                        <option value="terminate">Terminate (kill session)</option>
                      </select>
                    </div>
                    {rule.type === 'content_match' ? (
                      <div class="settings-item full-width">
                        <label class="settings-label">Patterns (one regex per line)</label>
                        <textarea
                          class="settings-textarea"
                          value={(rule.patterns || []).join('\n')}
                          onChange={(e) => updateCustomRule(index, 'patterns', e.target.value.split('\n').filter(p => p.trim()))}
                          placeholder="Enter regex patterns, one per line"
                          rows={3}
                        />
                      </div>
                    ) : (
                      <div class="settings-item">
                        <label class="settings-label">Threshold</label>
                        <input
                          type="number"
                          class="settings-input"
                          value={rule.threshold || 0}
                          onChange={(e) => updateCustomRule(index, 'threshold', parseInt(e.target.value))}
                        />
                      </div>
                    )}
                    <div class="settings-item full-width">
                      <label class="settings-label">Description</label>
                      <input
                        type="text"
                        class="settings-input"
                        value={rule.description || ''}
                        onChange={(e) => updateCustomRule(index, 'description', e.target.value)}
                        placeholder="What does this rule detect?"
                      />
                    </div>
                  </div>
                </div>
              ))}
            </div>
          )}
        </div>
      </div>
    </>
  )
}

// ============================================================================
// Dashboard Page
// ============================================================================

function DashboardPage({ stats, flaggedStats, sparklineData }) {
  const flaggedClass = flaggedStats.critical > 0 ? 'error' : flaggedStats.warning > 0 ? 'warning' : ''

  return (
    <>
      <div class="metrics-row">
        <MetricCard
          label="Active Sessions"
          value={stats.active || 0}
          className="success"
          sparklineData={sparklineData.active}
        />
        <MetricCard
          label="Total Requests"
          value={stats.total_requests || 0}
          sparklineData={sparklineData.requests}
        />
        <MetricCard
          label="Bytes In"
          value={formatBytes(stats.total_bytes_in || 0)}
          sparklineData={sparklineData.bytesIn}
        />
        <MetricCard
          label="Bytes Out"
          value={formatBytes(stats.total_bytes_out || 0)}
          sparklineData={sparklineData.bytesOut}
        />
        <MetricCard
          label="Killed"
          value={stats.killed || 0}
          className="error"
        />
        <MetricCard
          label="Flagged"
          value={flaggedStats.total_flagged || 0}
          className={flaggedClass}
        />
      </div>
    </>
  )
}

// ============================================================================
// Main App Component
// ============================================================================

export function App() {
  const [authed, setAuthed] = useState(!!localStorage.getItem(AUTH_KEY))
  const [checking, setChecking] = useState(!authed)

  useEffect(() => {
    setLogoutHandler(() => setAuthed(false))
    if (!authed) {
      fetch(API_BASE + '/control/health')
        .then((res) => {
          if (res.ok) {
            setAuthed(true)
          }
        })
        .catch(() => {})
        .finally(() => setChecking(false))
    }
  }, [])

  if (checking) return null

  if (!authed) {
    return <Login onLogin={() => setAuthed(true)} />
  }

  return <AppShell />
}

function AppShell() {
  const [page, setPage] = useState('dashboard')
  const [stats, setStats] = useState({})
  const [sessions, setSessions] = useState([])
  const [history, setHistory] = useState([])
  const [flagged, setFlagged] = useState([])
  const [flaggedStats, setFlaggedStats] = useState({})
  const [voiceSessions, setVoiceSessions] = useState([])
  const [voiceHistory, setVoiceHistory] = useState([])
  const [selectedFlagged, setSelectedFlagged] = useState(null)
  const [selectedSession, setSelectedSession] = useState(null)
  const [selectedVoice, setSelectedVoice] = useState(null)
  const [status, setStatus] = useState('connecting')
  const [lastUpdated, setLastUpdated] = useState(null)
  const [isRefreshing, setIsRefreshing] = useState(false)
  const [searchTerm, setSearchTerm] = useState('')
  const [historyPage, setHistoryPage] = useState(0)
  const [historyTotal, setHistoryTotal] = useState(0)
  const historyPageSize = 50

  // Sparkline data (last 20 values)
  const sparklineData = useRef({
    active: [],
    requests: [],
    bytesIn: [],
    bytesOut: [],
  })

  const updateSparkline = (key, value) => {
    const data = sparklineData.current[key]
    data.push(value)
    if (data.length > 20) data.shift()
  }

  const fetchStats = async () => {
    try {
      const res = await apiFetch(API_BASE + '/control/stats')
      const data = await res.json()
      setStats(data)
      updateSparkline('active', data.active || 0)
      updateSparkline('requests', data.total_requests || 0)
      updateSparkline('bytesIn', data.total_bytes_in || 0)
      updateSparkline('bytesOut', data.total_bytes_out || 0)
    } catch (err) {
      console.error('Failed to fetch stats:', err)
    }
  }

  const fetchSessions = async () => {
    try {
      const res = await apiFetch(API_BASE + '/control/sessions')
      const data = await res.json()
      setSessions(data.sessions || [])
    } catch (err) {
      console.error('Failed to fetch sessions:', err)
    }
  }

  const fetchHistory = async (pageNum = historyPage) => {
    try {
      const offset = pageNum * historyPageSize
      const res = await apiFetch(API_BASE + '/control/history?limit=' + historyPageSize + '&offset=' + offset)
      const data = await res.json()
      setHistory(data.sessions || [])
      setHistoryTotal(data.total_count || 0)
    } catch (err) {
      console.error('Failed to fetch history:', err)
    }
  }

  const fetchFlagged = async () => {
    try {
      const res = await apiFetch(API_BASE + '/control/flagged')
      if (res.status === 503) {
        setFlagged([])
        return
      }
      const data = await res.json()
      setFlagged(data.flagged || [])
    } catch (err) {
      console.error('Failed to fetch flagged:', err)
    }
  }

  const fetchFlaggedStats = async () => {
    try {
      const res = await apiFetch(API_BASE + '/control/flagged/stats')
      if (res.status === 503) {
        setFlaggedStats({})
        return
      }
      const data = await res.json()
      setFlaggedStats(data)
    } catch (err) {
      console.error('Failed to fetch flagged stats:', err)
    }
  }

  const fetchVoiceSessions = async () => {
    try {
      // Fetch active voice sessions
      const res = await apiFetch(API_BASE + '/control/voice')
      if (res.ok) {
        const data = await res.json()
        setVoiceSessions(data.voice_sessions || [])
      }
    } catch (err) {
      console.error('Failed to fetch voice sessions:', err)
    }
  }

  const fetchVoiceHistory = async () => {
    try {
      const res = await apiFetch(API_BASE + '/control/voice-history')
      if (res.ok) {
        const data = await res.json()
        setVoiceHistory(data.voice_sessions || [])
      }
    } catch (err) {
      console.error('Failed to fetch voice history:', err)
    }
  }

  const checkHealth = async () => {
    try {
      const res = await apiFetch(API_BASE + '/control/health')
      const data = await res.json()
      setStatus(data.status === 'ok' ? 'connected' : 'error')
    } catch {
      setStatus('disconnected')
    }
  }

  const killSession = async (id) => {
    if (!confirm('Kill this session?')) return
    try {
      await apiFetch(API_BASE + '/control/sessions/' + id + '/kill', { method: 'POST' })
      fetchSessions()
      fetchStats()
    } catch {
      alert('Failed to kill session')
    }
  }

  const refreshData = async () => {
    setIsRefreshing(true)
    await Promise.all([
      fetchStats(),
      fetchSessions(),
      fetchFlaggedStats(),
    ])
    setLastUpdated(new Date().toLocaleTimeString())
    setIsRefreshing(false)
  }

  useEffect(() => {
    const controller = new AbortController()

    // Initial data load for current page
    refreshData()
    checkHealth()
    if (page === 'history') { setHistoryPage(0); fetchHistory(0) }
    if (page === 'flagged') {
      fetchFlagged()
      fetchFlaggedStats()
    }
    if (page === 'voice') {
      fetchVoiceSessions()
      fetchVoiceHistory()
    }
    setSearchTerm('')

    // Polling intervals (skip when tab is not visible to reduce load)
    const interval = setInterval(() => {
      if (controller.signal.aborted || document.hidden) return
      refreshData()
      if (page === 'flagged') fetchFlagged()
      if (page === 'voice') {
        fetchVoiceSessions()
        fetchVoiceHistory()
      }
    }, 5000)

    const healthInterval = setInterval(checkHealth, 10000)

    return () => {
      controller.abort()
      clearInterval(interval)
      clearInterval(healthInterval)
    }
  }, [page])

  const getPageTitle = () => {
    switch (page) {
      case 'dashboard': return 'Dashboard'
      case 'sessions': return 'Live Sessions'
      case 'flagged': return 'Flagged Sessions'
      case 'voice': return 'Voice Sessions'
      case 'history': return 'Session History'
      case 'settings': return 'Settings'
      default: return 'ELIDA'
    }
  }

  // Combine active and historical voice sessions for the voice tab
  const allVoiceSessions = [...voiceSessions, ...voiceHistory]

  return (
    <div class="app-layout">
      <Sidebar
        activePage={page}
        onNavigate={setPage}
        flaggedCount={flaggedStats.total_flagged || 0}
      />

      <TopBar
        title={getPageTitle()}
        status={status}
        lastUpdated={lastUpdated}
        isRefreshing={isRefreshing}
      />

      <main class="main-content">
        {page === 'dashboard' && (
          <DashboardPage
            stats={stats}
            flaggedStats={flaggedStats}
            sparklineData={sparklineData.current}
          />
        )}

        {(page === 'dashboard' || page === 'sessions') && (
          <div class="panel">
            <div class="panel-header">
              <h2 class="panel-title">
                {page === 'dashboard' ? 'Recent Sessions' : 'Live Sessions'}
              </h2>
              <div class="panel-actions">
                <SearchInput
                  value={searchTerm}
                  onChange={setSearchTerm}
                  placeholder="Search sessions..."
                />
              </div>
            </div>
            <div class="panel-body no-padding">
              <SessionTable
                sessions={sessions}
                showActions={true}
                onKill={killSession}
                searchTerm={searchTerm}
              />
            </div>
          </div>
        )}

        {page === 'flagged' && (
          <div class="panel">
            <div class="panel-header">
              <h2 class="panel-title">Flagged Sessions</h2>
              <div class="panel-actions">
                <SearchInput
                  value={searchTerm}
                  onChange={setSearchTerm}
                  placeholder="Search flagged..."
                />
              </div>
            </div>
            <div class="panel-body">
              <div class="flagged-summary">
                <span class="flagged-stat critical">{flaggedStats.critical || 0} Critical</span>
                <span class="flagged-stat warning">{flaggedStats.warning || 0} Warning</span>
                <span class="flagged-stat info">{flaggedStats.info || 0} Info</span>
              </div>
            </div>
            <div class="panel-body no-padding">
              <FlaggedTable
                flagged={flagged}
                onViewDetails={setSelectedFlagged}
                searchTerm={searchTerm}
              />
            </div>
          </div>
        )}

        {page === 'voice' && (
          <div class="panel">
            <div class="panel-header">
              <h2 class="panel-title">Voice Sessions</h2>
              <div class="panel-actions">
                <SearchInput
                  value={searchTerm}
                  onChange={setSearchTerm}
                  placeholder="Search voice sessions..."
                />
              </div>
            </div>
            <div class="panel-body no-padding">
              <VoiceTable
                voiceSessions={allVoiceSessions}
                onViewDetails={setSelectedVoice}
                searchTerm={searchTerm}
              />
            </div>
          </div>
        )}

        {page === 'history' && (
          <div class="panel">
            <div class="panel-header">
              <h2 class="panel-title">Session History</h2>
              <div class="panel-actions">
                <SearchInput
                  value={searchTerm}
                  onChange={setSearchTerm}
                  placeholder="Search history..."
                />
              </div>
            </div>
            <div class="panel-body no-padding">
              <SessionTable
                sessions={history}
                showActions={false}
                onViewDetails={setSelectedSession}
                searchTerm={searchTerm}
              />
            </div>
            {historyTotal > historyPageSize && (
              <div class="panel-footer" style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', padding: '0.75rem 1rem' }}>
                <span style={{ fontSize: '0.85rem', color: 'var(--text-secondary)' }}>
                  Showing {historyPage * historyPageSize + 1}–{Math.min((historyPage + 1) * historyPageSize, historyTotal)} of {historyTotal}
                </span>
                <div style={{ display: 'flex', gap: '0.5rem' }}>
                  <button
                    class="btn btn-secondary btn-sm"
                    disabled={historyPage === 0}
                    onClick={() => { const p = historyPage - 1; setHistoryPage(p); fetchHistory(p) }}
                  >
                    Previous
                  </button>
                  <button
                    class="btn btn-secondary btn-sm"
                    disabled={(historyPage + 1) * historyPageSize >= historyTotal}
                    onClick={() => { const p = historyPage + 1; setHistoryPage(p); fetchHistory(p) }}
                  >
                    Next
                  </button>
                </div>
              </div>
            )}
          </div>
        )}

        {page === 'settings' && <SettingsPage />}
      </main>

      {selectedFlagged && (
        <FlaggedDetails flagged={selectedFlagged} onClose={() => setSelectedFlagged(null)} />
      )}

      {selectedSession && (
        <SessionDetails session={selectedSession} onClose={() => setSelectedSession(null)} />
      )}

      {selectedVoice && (
        <VoiceDetails voiceSession={selectedVoice} onClose={() => setSelectedVoice(null)} />
      )}
    </div>
  )
}
