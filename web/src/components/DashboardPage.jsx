import { useState, useEffect, useRef } from 'preact/hooks'
import { apiFetch } from '../apiFetch'
import { formatBytes, formatDuration, truncateId } from '../utils'
import { StateBadge } from './shared/Badge'
import { SessionDetailModal } from './SessionDetailModal'

// ---------------------------------------------------------------------------
// Simple SVG line chart — requests over time
// ---------------------------------------------------------------------------

function RequestsChart({ points }) {
  if (!points || points.length < 2) {
    return <div class="chart-empty">No data for the last 24 hours</div>
  }

  const W = 700, H = 160, PAD = { top: 10, right: 10, bottom: 24, left: 45 }
  const plotW = W - PAD.left - PAD.right
  const plotH = H - PAD.top - PAD.bottom

  const maxReq = Math.max(...points.map(p => p.request_count), 1)
  const xScale = (i) => PAD.left + (i / (points.length - 1)) * plotW
  const yScale = (v) => PAD.top + plotH - (v / maxReq) * plotH

  const linePath = points
    .map((p, i) => `${i === 0 ? 'M' : 'L'}${xScale(i).toFixed(1)},${yScale(p.request_count).toFixed(1)}`)
    .join(' ')

  const areaPath = linePath +
    ` L${xScale(points.length - 1).toFixed(1)},${(PAD.top + plotH).toFixed(1)}` +
    ` L${xScale(0).toFixed(1)},${(PAD.top + plotH).toFixed(1)} Z`

  // X-axis labels (show ~6 labels)
  const labelInterval = Math.max(1, Math.floor(points.length / 6))
  const xLabels = points
    .filter((_, i) => i % labelInterval === 0 || i === points.length - 1)
    .map((p, _, arr) => ({
      x: xScale(points.indexOf(p)),
      label: new Date(p.timestamp).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }),
    }))

  // Y-axis labels (0, mid, max)
  const yLabels = [0, Math.round(maxReq / 2), maxReq].map(v => ({
    y: yScale(v),
    label: v >= 1000 ? (v / 1000).toFixed(1) + 'k' : String(v),
  }))

  return (
    <svg viewBox={`0 0 ${W} ${H}`} class="requests-chart">
      <defs>
        <linearGradient id="areaGrad" x1="0" x2="0" y1="0" y2="1">
          <stop offset="0%" stop-color="#6366f1" stop-opacity="0.3" />
          <stop offset="100%" stop-color="#6366f1" stop-opacity="0.02" />
        </linearGradient>
      </defs>

      {/* Grid lines */}
      {yLabels.map((yl, i) => (
        <line key={i} x1={PAD.left} x2={W - PAD.right} y1={yl.y} y2={yl.y}
          stroke="var(--border-color)" stroke-width="0.5" />
      ))}

      {/* Area fill */}
      <path d={areaPath} fill="url(#areaGrad)" />

      {/* Line */}
      <path d={linePath} fill="none" stroke="#6366f1" stroke-width="2" />

      {/* Y labels */}
      {yLabels.map((yl, i) => (
        <text key={i} x={PAD.left - 6} y={yl.y + 3} text-anchor="end"
          fill="var(--text-dim)" font-size="9" font-family="'SF Mono', monospace">
          {yl.label}
        </text>
      ))}

      {/* X labels */}
      {xLabels.map((xl, i) => (
        <text key={i} x={xl.x} y={H - 4} text-anchor="middle"
          fill="var(--text-dim)" font-size="9" font-family="'SF Mono', monospace">
          {xl.label}
        </text>
      ))}
    </svg>
  )
}

// ---------------------------------------------------------------------------
// KPI Card
// ---------------------------------------------------------------------------

function KPICard({ label, value, sub, className }) {
  return (
    <div class={'kpi-card' + (className ? ' ' + className : '')}>
      <div class="kpi-value">{value}</div>
      <div class="kpi-label">{label}</div>
      {sub && <div class="kpi-sub">{sub}</div>}
    </div>
  )
}

// ---------------------------------------------------------------------------
// Dashboard Page
// ---------------------------------------------------------------------------

export function DashboardPage() {
  const [stats, setStats] = useState({})
  const [flaggedStats, setFlaggedStats] = useState({})
  const [timeseries, setTimeseries] = useState([])
  const [sessions, setSessions] = useState([])
  const [detailSession, setDetailSession] = useState(null)
  const timerRef = useRef(null)

  const fetchAll = async () => {
    try {
      const [statsRes, flaggedRes, tsRes, sessRes] = await Promise.all([
        apiFetch('/control/stats'),
        apiFetch('/control/flagged/stats'),
        apiFetch('/control/history/timeseries?interval=hour'),
        apiFetch('/control/sessions'),
      ])

      if (statsRes.ok) setStats(await statsRes.json())
      if (flaggedRes.ok) setFlaggedStats(await flaggedRes.json())
      if (tsRes.ok) {
        const data = await tsRes.json()
        setTimeseries(data.points || [])
      }
      if (sessRes.ok) {
        const data = await sessRes.json()
        // Sort: active first, then by start_time desc, take last 10
        const sorted = (data.sessions || [])
          .sort((a, b) => {
            if (a.state === 'active' && b.state !== 'active') return -1
            if (a.state !== 'active' && b.state === 'active') return 1
            return new Date(b.start_time || 0) - new Date(a.start_time || 0)
          })
          .slice(0, 10)
        setSessions(sorted)
      }
    } catch (err) {
      console.error('Dashboard fetch error:', err)
    }
  }

  useEffect(() => {
    fetchAll()
    timerRef.current = setInterval(() => {
      if (!document.hidden) fetchAll()
    }, 5000)
    return () => clearInterval(timerRef.current)
  }, [])

  const killSession = async (id) => {
    try {
      await apiFetch('/control/sessions/' + id + '/kill', { method: 'POST' })
      fetchAll()
    } catch {}
  }

  // Backend summary from sessions
  const backendMap = {}
  sessions.forEach(s => {
    const entries = s.backends_used && Object.keys(s.backends_used).length > 0
      ? Object.entries(s.backends_used)
      : [[s.backend || 'unknown', s.request_count || 0]]
    entries.forEach(([name, count]) => {
      if (!backendMap[name]) backendMap[name] = { active: 0, requests: 0 }
      backendMap[name].requests += count
      if (s.state === 'active') backendMap[name].active++
    })
  })
  const backendRows = Object.entries(backendMap).sort((a, b) => b[1].requests - a[1].requests)

  const totalRequests24h = timeseries.reduce((sum, p) => sum + (p.request_count || 0), 0)
  const flaggedKilled = (flaggedStats.total_flagged || 0) + (stats.killed || 0)

  return (
    <div class="dashboard-page">

      {/* KPIs */}
      <div class="kpi-row">
        <KPICard
          label="Active Sessions"
          value={stats.active || 0}
          className="kpi-accent"
        />
        <KPICard
          label="Requests (24h)"
          value={totalRequests24h.toLocaleString()}
        />
        <KPICard
          label="Flagged / Killed"
          value={flaggedKilled}
          sub={`${flaggedStats.total_flagged || 0} flagged \u00B7 ${stats.killed || 0} killed`}
          className={flaggedStats.critical > 0 ? 'kpi-danger' : ''}
        />
        <KPICard
          label="Total Sessions"
          value={(stats.total || 0).toLocaleString()}
        />
      </div>

      {/* Requests chart */}
      <div class="dashboard-chart-panel">
        <div class="dashboard-chart-header">
          <span class="dashboard-chart-title">Requests (24h)</span>
        </div>
        <RequestsChart points={timeseries} />
      </div>

      {/* Two column: backends + recent sessions */}
      <div class="dashboard-grid">

        {/* Sessions by Backend */}
        <div class="dashboard-panel">
          <div class="dashboard-panel-header">Sessions by Backend</div>
          {backendRows.length === 0 ? (
            <div class="dashboard-panel-empty">No backend data</div>
          ) : (
            <table class="dashboard-table">
              <thead>
                <tr>
                  <th>Backend</th>
                  <th>Active</th>
                  <th>Requests</th>
                </tr>
              </thead>
              <tbody>
                {backendRows.map(([name, data]) => (
                  <tr key={name}>
                    <td class="mono">{name}</td>
                    <td>{data.active}</td>
                    <td>{data.requests}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>

        {/* Recent Sessions */}
        <div class="dashboard-panel">
          <div class="dashboard-panel-header">Recent Sessions</div>
          {sessions.length === 0 ? (
            <div class="dashboard-panel-empty">No sessions yet</div>
          ) : (
            <table class="dashboard-table">
              <thead>
                <tr>
                  <th>Session</th>
                  <th>State</th>
                  <th>Requests</th>
                  <th>Duration</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                {sessions.map(s => (
                  <tr key={s.id} class="dashboard-session-row" onClick={() => setDetailSession(s)}>
                    <td class="mono">{truncateId(s.id)}</td>
                    <td><StateBadge state={s.state} /></td>
                    <td>{s.request_count}</td>
                    <td>{s.duration}</td>
                    <td>
                      {s.state === 'active' && (
                        <button
                          class="btn btn-danger btn-xs"
                          onClick={(e) => { e.stopPropagation(); killSession(s.id); }}
                        >
                          Kill
                        </button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      </div>

      {detailSession && (
        <SessionDetailModal
          session={detailSession}
          onClose={() => setDetailSession(null)}
          onKill={(id) => { killSession(id); setDetailSession(null); }}
        />
      )}
    </div>
  )
}
