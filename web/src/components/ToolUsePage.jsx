import { useState, useEffect, useRef } from 'preact/hooks'
import { apiFetch } from '../apiFetch'
import { truncateId } from '../utils'
import { StateBadge } from './shared/Badge'
import { SessionDetailModal } from './SessionDetailModal'

// Risk tiers — same as SessionTurns
const TOOL_TIERS = {
  Read: 'safe', Glob: 'safe', Grep: 'safe', WebFetch: 'safe', WebSearch: 'safe',
  Edit: 'modify', Write: 'modify', MultiEdit: 'modify', NotebookEdit: 'modify',
  Agent: 'delegate', Task: 'delegate', TaskCreate: 'delegate', TeamCreate: 'delegate', Skill: 'delegate',
  Bash: 'danger', Execute: 'danger',
}

const TIER_META = {
  safe:     { label: 'Read-only',     color: '#71717a', bg: 'rgba(113,113,122,0.15)', text: '#a1a1aa' },
  modify:   { label: 'Modification',  color: '#eab308', bg: 'rgba(234,179,8,0.12)',   text: '#fde68a' },
  delegate: { label: 'Delegation',    color: '#8b5cf6', bg: 'rgba(139,92,246,0.12)',  text: '#c4b5fd' },
  danger:   { label: 'Shell/Exec',    color: '#f59e0b', bg: 'rgba(245,158,11,0.15)',  text: '#fcd34d' },
  unknown:  { label: 'Unknown',       color: '#f43f5e', bg: 'rgba(244,63,94,0.12)',   text: '#fda4af' },
}

function ToolPill({ name, count }) {
  const tier = TOOL_TIERS[name] || 'unknown'
  const meta = TIER_META[tier]
  return (
    <span
      class="tool-pill"
      style={{ background: meta.bg, color: meta.text, borderColor: meta.color }}
    >
      {name} {count > 1 ? count : ''}
    </span>
  )
}

// ---------------------------------------------------------------------------
// Scorecard
// ---------------------------------------------------------------------------

function Scorecard({ label, value, sub, className }) {
  return (
    <div class={'scorecard' + (className ? ' ' + className : '')}>
      <div class="scorecard-label">{label}</div>
      <div class="scorecard-value">{value}</div>
      {sub && <div class="scorecard-sub">{sub}</div>}
    </div>
  )
}

// ---------------------------------------------------------------------------
// Horizontal bar chart — tool usage breakdown
// ---------------------------------------------------------------------------

function ToolBarChart({ toolTotals }) {
  if (toolTotals.length === 0) return null

  const max = toolTotals[0][1]

  return (
    <div class="tool-bar-chart">
      {toolTotals.slice(0, 15).map(([name, count]) => {
        const tier = TOOL_TIERS[name] || 'unknown'
        const meta = TIER_META[tier]
        const pct = (count / max) * 100

        return (
          <div key={name} class="tool-bar-row">
            <span class="tool-bar-name mono">{name}</span>
            <div class="tool-bar-track">
              <div
                class="tool-bar-fill"
                style={{ width: pct + '%', background: meta.color }}
              />
            </div>
            <span class="tool-bar-count">{count}</span>
          </div>
        )
      })}
    </div>
  )
}

// ---------------------------------------------------------------------------
// Tool Use Page
// ---------------------------------------------------------------------------

export function ToolUsePage() {
  const [sessions, setSessions] = useState([])
  const [filter, setFilter] = useState('all') // all | dangerous | flagged
  const [detailSession, setDetailSession] = useState(null)
  const timerRef = useRef(null)

  const fetchSessions = async () => {
    try {
      const res = await apiFetch('/control/sessions')
      const data = await res.json()
      setSessions(data.sessions || [])
    } catch (err) {
      console.error('Failed to fetch sessions:', err)
    }
  }

  useEffect(() => {
    fetchSessions()
    timerRef.current = setInterval(() => {
      if (!document.hidden) fetchSessions()
    }, 5000)
    return () => clearInterval(timerRef.current)
  }, [])

  const killSession = async (id) => {
    try {
      await apiFetch('/control/sessions/' + id + '/kill', { method: 'POST' })
      fetchSessions()
    } catch {}
  }

  // Aggregate tool counts across all sessions
  const globalToolCounts = {}
  let totalToolCalls = 0
  let sessionsWithTools = 0
  let highestRiskTool = null
  let highestRiskCount = 0

  sessions.forEach(s => {
    const counts = s.tool_call_counts || {}
    if (Object.keys(counts).length > 0) sessionsWithTools++
    Object.entries(counts).forEach(([name, count]) => {
      globalToolCounts[name] = (globalToolCounts[name] || 0) + count
      totalToolCalls += count
      const tier = TOOL_TIERS[name] || 'unknown'
      if ((tier === 'danger' || tier === 'unknown') && count > highestRiskCount) {
        highestRiskTool = name
        highestRiskCount = count
      }
    })
  })

  const toolTotals = Object.entries(globalToolCounts).sort((a, b) => b[1] - a[1])
  const mostUsed = toolTotals.length > 0 ? toolTotals[0] : null

  // Filter sessions for the table
  let filtered = sessions.filter(s => (s.tool_calls || 0) > 0)
  if (filter === 'dangerous') {
    filtered = filtered.filter(s => {
      const counts = s.tool_call_counts || {}
      return Object.keys(counts).some(name => {
        const tier = TOOL_TIERS[name] || 'unknown'
        return tier === 'danger' || tier === 'delegate'
      })
    })
  } else if (filter === 'flagged') {
    filtered = filtered.filter(s => (s.risk_score || 0) > 0)
  }

  // Sort: active first, then by tool_calls desc
  filtered.sort((a, b) => {
    if (a.state === 'active' && b.state !== 'active') return -1
    if (a.state !== 'active' && b.state === 'active') return 1
    return (b.tool_calls || 0) - (a.tool_calls || 0)
  })

  const filters = [
    { id: 'all', label: 'All' },
    { id: 'dangerous', label: 'Dangerous' },
    { id: 'flagged', label: 'Flagged' },
  ]

  return (
    <div class="tooluse-page">

      {/* Scorecard row */}
      <div class="scorecard-row">
        <Scorecard
          label="Total Tool Calls"
          value={totalToolCalls.toLocaleString()}
          sub={`across ${sessionsWithTools} sessions`}
        />
        <Scorecard
          label="Most-used Tool"
          value={mostUsed ? mostUsed[0] : '\u2014'}
          sub={mostUsed ? mostUsed[1].toLocaleString() + ' calls' : ''}
        />
        <Scorecard
          label="Highest-risk Tool"
          value={highestRiskTool || '\u2014'}
          sub={highestRiskTool ? highestRiskCount + ' calls' : 'none detected'}
          className={highestRiskTool ? 'scorecard-warn' : ''}
        />
        <Scorecard
          label="Unique Tools"
          value={toolTotals.length}
        />
      </div>

      {/* Tool breakdown chart */}
      <div class="tooluse-chart-panel">
        <div class="tooluse-chart-header">
          <span class="tooluse-chart-title">Tool Breakdown</span>
          <div class="tooluse-legend">
            {Object.entries(TIER_META).filter(([k]) => k !== 'unknown').map(([tier, meta]) => (
              <span key={tier} class="tooluse-legend-item">
                <span class="tooluse-legend-dot" style={{ background: meta.color }} />
                {meta.label}
              </span>
            ))}
          </div>
        </div>
        {toolTotals.length === 0 ? (
          <div class="tooluse-empty">No tool calls recorded</div>
        ) : (
          <ToolBarChart toolTotals={toolTotals} />
        )}
      </div>

      {/* Session table with tool pills */}
      <div class="tooluse-table-panel">
        <div class="tooluse-table-header">
          <span class="tooluse-chart-title">Sessions with Tool Use</span>
          <div class="sessions-filters">
            {filters.map(f => (
              <button
                key={f.id}
                class={'filter-chip' + (filter === f.id ? ' active' : '')}
                onClick={() => setFilter(f.id)}
              >
                {f.label}
              </button>
            ))}
          </div>
        </div>

        {filtered.length === 0 ? (
          <div class="tooluse-empty">No sessions with tool calls</div>
        ) : (
          <table class="dashboard-table">
            <thead>
              <tr>
                <th>Session</th>
                <th>State</th>
                <th>Tools</th>
                <th>Tool Breakdown</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {filtered.map(s => {
                const counts = s.tool_call_counts || {}
                const entries = Object.entries(counts).sort((a, b) => b[1] - a[1])
                const shown = entries.slice(0, 4)
                const remaining = entries.length - shown.length

                return (
                  <tr key={s.id} class="dashboard-session-row" onClick={() => setDetailSession(s)}>
                    <td class="mono">{truncateId(s.id)}</td>
                    <td><StateBadge state={s.state} /></td>
                    <td>{s.tool_calls || 0}</td>
                    <td class="tool-pills-cell">
                      {shown.map(([name, count]) => (
                        <ToolPill key={name} name={name} count={count} />
                      ))}
                      {remaining > 0 && (
                        <span class="tool-pill-more">+{remaining} more</span>
                      )}
                    </td>
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
                )
              })}
            </tbody>
          </table>
        )}
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
