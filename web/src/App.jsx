import { useState, useEffect } from 'preact/hooks'

const API_BASE = ''

function formatBytes(bytes) {
  if (bytes === 0) return '0 B'
  const k = 1024
  const sizes = ['B', 'KB', 'MB', 'GB']
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  return parseFloat((bytes / Math.pow(k, i)).toFixed(1)) + ' ' + sizes[i]
}

function truncateId(id) {
  return id ? id.substring(0, 8) + '...' : '-'
}

function StatCard({ label, value, className }) {
  return (
    <div class="stat-card">
      <div class="stat-label">{label}</div>
      <div class={'stat-value ' + (className || '')}>{value}</div>
    </div>
  )
}

function StateBadge({ state }) {
  return <span class={'state-badge state-' + state}>{state}</span>
}

function SeverityBadge({ severity }) {
  return <span class={'severity-badge severity-' + severity}>{severity}</span>
}

function SessionTable({ sessions, showActions, onKill }) {
  if (!sessions || sessions.length === 0) {
    return <div class="empty-state">No sessions</div>
  }

  return (
    <table>
      <thead>
        <tr>
          <th>Session ID</th>
          <th>State</th>
          <th>Backend</th>
          <th>Client</th>
          <th>Requests</th>
          <th>Bytes In/Out</th>
          <th>Duration</th>
          {showActions && <th>Action</th>}
        </tr>
      </thead>
      <tbody>
        {sessions.map((s) => (
          <tr key={s.id}>
            <td class="mono">{truncateId(s.id)}</td>
            <td><StateBadge state={s.state} /></td>
            <td class="mono muted">{new URL(s.backend).host}</td>
            <td class="mono muted">{s.client_addr}</td>
            <td>{s.request_count}</td>
            <td class="mono">{formatBytes(s.bytes_in)} / {formatBytes(s.bytes_out)}</td>
            <td class="mono">{s.duration_ms ? (s.duration_ms / 1000).toFixed(1) + 's' : s.duration}</td>
            {showActions && (
              <td>
                <button class="btn-danger" onClick={() => onKill(s.id)} disabled={s.state !== 'active'}>
                  Kill
                </button>
              </td>
            )}
          </tr>
        ))}
      </tbody>
    </table>
  )
}

function FlaggedTable({ flagged, onViewDetails }) {
  if (!flagged || flagged.length === 0) {
    return <div class="empty-state">No flagged sessions - all sessions within policy</div>
  }

  return (
    <table>
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
        {flagged.map((f) => (
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
              <button class="btn-secondary" onClick={() => onViewDetails(f)}>
                View
              </button>
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}

function FlaggedDetails({ flagged, onClose }) {
  if (!flagged) return null

  return (
    <div class="modal-overlay" onClick={onClose}>
      <div class="modal" onClick={(e) => e.stopPropagation()}>
        <div class="modal-header">
          <h2>Flagged Session Details</h2>
          <button class="btn-close" onClick={onClose}>x</button>
        </div>
        <div class="modal-body">
          <div class="detail-row">
            <strong>Session ID:</strong> <span class="mono">{flagged.session_id}</span>
          </div>
          <div class="detail-row">
            <strong>Max Severity:</strong> <SeverityBadge severity={flagged.max_severity} />
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
                <div class="violation-stats">
                  Threshold: {v.threshold} | Actual: {v.actual_value}
                </div>
              </div>
            ))}
          </div>

          {flagged.captured_content && flagged.captured_content.length > 0 && (
            <div>
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
                  </div>
                ))}
              </div>
            </div>
          )}
        </div>
      </div>
    </div>
  )
}

export function App() {
  const [tab, setTab] = useState('live')
  const [stats, setStats] = useState({})
  const [sessions, setSessions] = useState([])
  const [history, setHistory] = useState([])
  const [flagged, setFlagged] = useState([])
  const [flaggedStats, setFlaggedStats] = useState({})
  const [selectedFlagged, setSelectedFlagged] = useState(null)
  const [status, setStatus] = useState('connecting')

  const fetchStats = async () => {
    try {
      const res = await fetch(API_BASE + '/control/stats')
      const data = await res.json()
      setStats(data)
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

  useEffect(() => {
    fetchStats()
    fetchSessions()
    fetchFlaggedStats()
    checkHealth()

    const interval = setInterval(() => {
      fetchStats()
      fetchSessions()
      if (tab === 'flagged') {
        fetchFlagged()
        fetchFlaggedStats()
      }
    }, 2000)

    const healthInterval = setInterval(checkHealth, 10000)

    return () => {
      clearInterval(interval)
      clearInterval(healthInterval)
    }
  }, [tab])

  useEffect(() => {
    if (tab === 'history') fetchHistory()
    if (tab === 'flagged') {
      fetchFlagged()
      fetchFlaggedStats()
    }
  }, [tab])

  const flaggedClass = flaggedStats.critical > 0 ? 'critical' : flaggedStats.warning > 0 ? 'warning' : ''

  return (
    <div class="app">
      <header class="header">
        <div>
          <h1>ELIDA</h1>
          <div class="subtitle">Edge Layer for Intelligent Defense of Agents</div>
        </div>
        <div class="status-indicator">
          <div class={'status-dot ' + status}></div>
          <span>{status === 'connected' ? 'Connected' : status === 'disconnected' ? 'Disconnected' : 'Connecting...'}</span>
        </div>
      </header>

      <div class="container">
        <div class="stats-grid">
          <StatCard label="Active Sessions" value={stats.active || 0} className="active" />
          <StatCard label="Total Requests" value={stats.total_requests || 0} />
          <StatCard label="Bytes In" value={formatBytes(stats.total_bytes_in || 0)} />
          <StatCard label="Bytes Out" value={formatBytes(stats.total_bytes_out || 0)} />
          <StatCard label="Killed" value={stats.killed || 0} className="killed" />
          <StatCard label="Flagged" value={flaggedStats.total_flagged || 0} className={flaggedClass} />
        </div>

        <div class="tabs">
          <button class={'tab ' + (tab === 'live' ? 'active' : '')} onClick={() => setTab('live')}>
            Live Sessions
          </button>
          <button class={'tab ' + (tab === 'flagged' ? 'active' : '')} onClick={() => setTab('flagged')}>
            Flagged ({flaggedStats.total_flagged || 0})
          </button>
          <button class={'tab ' + (tab === 'history' ? 'active' : '')} onClick={() => setTab('history')}>
            History
          </button>
        </div>

        {tab === 'live' && (
          <div class="panel">
            <div class="refresh-info">Auto-refreshes every 2 seconds</div>
            <div class="table-container">
              <SessionTable sessions={sessions} showActions={true} onKill={killSession} />
            </div>
          </div>
        )}

        {tab === 'flagged' && (
          <div class="panel">
            <div class="flagged-summary">
              <span class="flagged-stat critical">{flaggedStats.critical || 0} Critical</span>
              <span class="flagged-stat warning">{flaggedStats.warning || 0} Warning</span>
              <span class="flagged-stat info">{flaggedStats.info || 0} Info</span>
            </div>
            <div class="table-container">
              <FlaggedTable flagged={flagged} onViewDetails={setSelectedFlagged} />
            </div>
          </div>
        )}

        {tab === 'history' && (
          <div class="panel">
            <div class="refresh-info">Historical sessions from database</div>
            <div class="table-container">
              <SessionTable sessions={history} showActions={false} />
            </div>
          </div>
        )}
      </div>

      {selectedFlagged && (
        <FlaggedDetails flagged={selectedFlagged} onClose={() => setSelectedFlagged(null)} />
      )}
    </div>
  )
}
