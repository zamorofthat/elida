import { useState, useEffect, useRef } from 'preact/hooks'
import { formatBytes, formatDuration, formatDurationStr, truncateId } from './utils'

const API_BASE = ''

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
    if (session?.id) {
      // Try live flagged endpoint first, then history endpoint
      fetch(API_BASE + '/control/flagged/' + session.id)
        .then(res => res.ok ? res.json() : null)
        .then(data => {
          if (data) {
            setFlaggedInfo(data)
          } else {
            // Fall back to history endpoint for persisted sessions
            return fetch(API_BASE + '/control/history/' + session.id)
              .then(res => res.ok ? res.json() : null)
              .then(histData => setFlaggedInfo(histData))
          }
        })
        .catch(() => setFlaggedInfo(null))
    }
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
    if (flagged?.session_id) {
      fetch(API_BASE + '/control/sessions/' + flagged.session_id)
        .then(res => res.ok ? res.json() : null)
        .then(data => setSessionInfo(data))
        .catch(() => setSessionInfo(null))
    }
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
      // Try to get the full voice session with transcript
      fetch(API_BASE + '/control/voice/' + voiceSession.parent_session_id + '/' + voiceSession.id)
        .then(res => res.ok ? res.json() : null)
        .then(data => setFullSession(data || voiceSession))
        .catch(() => setFullSession(voiceSession))
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
      const res = await fetch(API_BASE + '/control/stats')
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
      const res = await fetch(API_BASE + '/control/sessions')
      const data = await res.json()
      setSessions(data.sessions || [])
    } catch (err) {
      console.error('Failed to fetch sessions:', err)
    }
  }

  const fetchHistory = async () => {
    try {
      const res = await fetch(API_BASE + '/control/history?limit=50')
      const data = await res.json()
      setHistory(data.sessions || [])
    } catch (err) {
      console.error('Failed to fetch history:', err)
    }
  }

  const fetchFlagged = async () => {
    try {
      const res = await fetch(API_BASE + '/control/flagged')
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
      const res = await fetch(API_BASE + '/control/flagged/stats')
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
      const res = await fetch(API_BASE + '/control/voice')
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
      const res = await fetch(API_BASE + '/control/voice-history')
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
      const res = await fetch(API_BASE + '/control/health')
      const data = await res.json()
      setStatus(data.status === 'ok' ? 'connected' : 'error')
    } catch {
      setStatus('disconnected')
    }
  }

  const killSession = async (id) => {
    if (!confirm('Kill this session?')) return
    try {
      await fetch(API_BASE + '/control/sessions/' + id + '/kill', { method: 'POST' })
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
    refreshData()
    checkHealth()

    const interval = setInterval(() => {
      refreshData()
      if (page === 'flagged') {
        fetchFlagged()
      }
      if (page === 'voice') {
        fetchVoiceSessions()
        fetchVoiceHistory()
      }
    }, 2000)

    const healthInterval = setInterval(checkHealth, 10000)

    return () => {
      clearInterval(interval)
      clearInterval(healthInterval)
    }
  }, [page])

  useEffect(() => {
    if (page === 'history') fetchHistory()
    if (page === 'flagged') {
      fetchFlagged()
      fetchFlaggedStats()
    }
    if (page === 'voice') {
      fetchVoiceSessions()
      fetchVoiceHistory()
    }
    setSearchTerm('')
  }, [page])

  const getPageTitle = () => {
    switch (page) {
      case 'dashboard': return 'Dashboard'
      case 'sessions': return 'Live Sessions'
      case 'flagged': return 'Flagged Sessions'
      case 'voice': return 'Voice Sessions'
      case 'history': return 'Session History'
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
          </div>
        )}
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
