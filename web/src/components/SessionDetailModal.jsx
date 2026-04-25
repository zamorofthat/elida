import { useState, useEffect, useRef } from 'preact/hooks'
import { apiFetch } from '../apiFetch'
import { formatBytes, formatDuration, formatDurationStr, truncateId } from '../utils'
import { StateBadge, SeverityBadge } from './shared/Badge'
import { IconX } from './shared/Icons'
import { SessionTurns } from './SessionTurns'

// ---------------------------------------------------------------------------
// Risk Score Chart — SVG area chart with threshold lines
// ---------------------------------------------------------------------------

function RiskScoreChart({ points }) {
  if (!points || points.length < 2) return null

  const W = 600, H = 120, PAD = { top: 8, right: 8, bottom: 20, left: 35 }
  const plotW = W - PAD.left - PAD.right
  const plotH = H - PAD.top - PAD.bottom

  const maxScore = Math.max(...points.map(p => p.score), 10)
  const xScale = (i) => PAD.left + (i / (points.length - 1)) * plotW
  const yScale = (v) => PAD.top + plotH - (v / maxScore) * plotH

  const linePath = points
    .map((p, i) => `${i === 0 ? 'M' : 'L'}${xScale(i).toFixed(1)},${yScale(p.score).toFixed(1)}`)
    .join(' ')

  const areaPath = linePath +
    ` L${xScale(points.length - 1).toFixed(1)},${(PAD.top + plotH).toFixed(1)}` +
    ` L${xScale(0).toFixed(1)},${(PAD.top + plotH).toFixed(1)} Z`

  // Threshold lines at common risk ladder values
  const thresholds = [
    { score: 10, label: 'warn', color: '#eab308' },
    { score: 30, label: 'throttle', color: '#f59e0b' },
    { score: 50, label: 'block', color: '#ef4444' },
  ].filter(t => t.score <= maxScore * 1.2)

  // Time labels
  const labelInterval = Math.max(1, Math.floor(points.length / 5))
  const xLabels = points
    .filter((_, i) => i % labelInterval === 0 || i === points.length - 1)
    .map(p => ({
      x: xScale(points.indexOf(p)),
      label: new Date(p.timestamp).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }),
    }))

  return (
    <svg viewBox={`0 0 ${W} ${H}`} class="risk-chart">
      <defs>
        <linearGradient id="riskGrad" x1="0" x2="0" y1="0" y2="1">
          <stop offset="0%" stop-color="#ef4444" stop-opacity="0.3" />
          <stop offset="100%" stop-color="#ef4444" stop-opacity="0.02" />
        </linearGradient>
      </defs>

      {/* Threshold lines */}
      {thresholds.map((t, i) => (
        <g key={i}>
          <line
            x1={PAD.left} x2={W - PAD.right}
            y1={yScale(t.score)} y2={yScale(t.score)}
            stroke={t.color} stroke-width="0.5" stroke-dasharray="4,3" opacity="0.5"
          />
          <text x={PAD.left - 4} y={yScale(t.score) + 3} text-anchor="end"
            fill={t.color} font-size="8" opacity="0.7" font-family="'SF Mono', monospace">
            {t.score}
          </text>
        </g>
      ))}

      {/* Area + line */}
      <path d={areaPath} fill="url(#riskGrad)" />
      <path d={linePath} fill="none" stroke="#ef4444" stroke-width="1.5" />

      {/* Current score dot */}
      {points.length > 0 && (
        <circle
          cx={xScale(points.length - 1)}
          cy={yScale(points[points.length - 1].score)}
          r="3" fill="#ef4444"
        />
      )}

      {/* X labels */}
      {xLabels.map((xl, i) => (
        <text key={i} x={xl.x} y={H - 4} text-anchor="middle"
          fill="var(--text-dim)" font-size="8" font-family="'SF Mono', monospace">
          {xl.label}
        </text>
      ))}
    </svg>
  )
}

// ---------------------------------------------------------------------------
// Collapsible Section
// ---------------------------------------------------------------------------

function Section({ title, defaultOpen, badge, children }) {
  const [open, setOpen] = useState(defaultOpen)
  return (
    <div class="detail-section">
      <button class="detail-section-header" onClick={() => setOpen(!open)}>
        <span class="detail-section-toggle">{open ? '\u25BE' : '\u25B8'}</span>
        <span class="detail-section-title">{title}</span>
        {badge != null && <span class="detail-section-badge">{badge}</span>}
      </button>
      {open && <div class="detail-section-body">{children}</div>}
    </div>
  )
}

// ---------------------------------------------------------------------------
// Timeline Band — horizontal dot strip
// ---------------------------------------------------------------------------

function TimelineBand({ turns }) {
  if (!turns || turns.length === 0) return null

  const typeColor = {
    message: '#6366f1',
    tool_call: '#f59e0b',
    violation: '#ef4444',
    summary: '#6b7280',
  }

  return (
    <div class="timeline-band">
      {turns.map((t, i) => (
        <span
          key={i}
          class="timeline-dot"
          style={{ background: typeColor[t.type] || '#6b7280' }}
          title={`${t.type}${t.tool_name ? ': ' + t.tool_name : ''}${t.role ? ' (' + t.role + ')' : ''}`}
        />
      ))}
    </div>
  )
}

// ---------------------------------------------------------------------------
// Session Detail Modal
// ---------------------------------------------------------------------------

export function SessionDetailModal({ session, onClose, onKill }) {
  const [turns, setTurns] = useState(null)
  const [flagged, setFlagged] = useState(null)
  const [riskCurve, setRiskCurve] = useState(null)
  const [liveSession, setLiveSession] = useState(session)
  const modalRef = useRef(null)

  // Fetch turns
  useEffect(() => {
    if (!session?.id) return
    const controller = new AbortController()

    apiFetch('/control/sessions/' + session.id + '/turns?full=true', { signal: controller.signal })
      .then(res => res.ok ? res.json() : null)
      .then(data => { if (!controller.signal.aborted) setTurns(data?.turns || []) })
      .catch(() => { if (!controller.signal.aborted) setTurns([]) })

    return () => controller.abort()
  }, [session?.id])

  // Fetch flagged info
  useEffect(() => {
    if (!session?.id) return
    const controller = new AbortController()

    apiFetch('/control/flagged/' + session.id, { signal: controller.signal })
      .then(res => res.ok ? res.json() : null)
      .then(data => { if (!controller.signal.aborted) setFlagged(data) })
      .catch(() => {})

    return () => controller.abort()
  }, [session?.id])

  // Fetch risk score curve
  useEffect(() => {
    if (!session?.id) return
    const controller = new AbortController()

    apiFetch('/control/sessions/' + session.id + '/risk-curve', { signal: controller.signal })
      .then(res => res.ok ? res.json() : null)
      .then(data => { if (!controller.signal.aborted) setRiskCurve(data?.points || []) })
      .catch(() => {})

    return () => controller.abort()
  }, [session?.id])

  // Poll live session data
  useEffect(() => {
    if (!session?.id || session.state !== 'active') return
    const controller = new AbortController()

    const poll = () => {
      apiFetch('/control/sessions/' + session.id, { signal: controller.signal })
        .then(res => res.ok ? res.json() : null)
        .then(data => {
          if (!controller.signal.aborted && data) setLiveSession(data)
        })
        .catch(() => {})
    }

    const id = setInterval(() => { if (!document.hidden) poll() }, 3000)
    return () => { clearInterval(id); controller.abort() }
  }, [session?.id, session?.state])

  // Escape key
  useEffect(() => {
    const onKey = (e) => { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  if (!session) return null

  const s = liveSession || session
  const violations = flagged?.violations || []
  const riskScore = flagged?.risk_score || s.risk_score || 0
  const currentAction = flagged?.current_action || s.current_action || ''

  // Compute tool summary from session data
  const toolCounts = s.tool_call_counts || {}
  const toolEntries = Object.entries(toolCounts).sort((a, b) => b[1] - a[1])
  const totalTools = s.tool_calls || toolEntries.reduce((sum, [, c]) => sum + c, 0)

  // Backend routing
  const backendsUsed = s.backends_used || {}
  const backendEntries = Object.entries(backendsUsed)
  const failedBackends = s.failed_backends || []

  return (
    <div class="modal-overlay" onClick={onClose}>
      <div class="modal detail-modal" onClick={(e) => e.stopPropagation()} ref={modalRef}>

        {/* Sticky header */}
        <div class="detail-header">
          <div class="detail-header-left">
            <StateBadge state={s.state} />
            <span class="mono detail-session-id">{truncateId(s.id, 16)}</span>
            {s.client_addr && <span class="detail-meta">{s.client_addr}</span>}
            {riskScore > 0 && (
              <span class={'detail-risk ' + (riskScore >= 30 ? 'risk-high' : riskScore >= 10 ? 'risk-med' : '')}>
                Risk {Math.round(riskScore)}
              </span>
            )}
            {currentAction && currentAction !== 'observe' && (
              <span class="detail-action-badge">{currentAction}</span>
            )}
          </div>
          <div class="detail-header-right">
            {s.state === 'active' && onKill && (
              <button
                class="btn btn-danger btn-sm"
                onClick={() => { if (confirm('Kill this session?')) onKill(s.id) }}
              >
                Kill
              </button>
            )}
            <button
              class="btn btn-secondary btn-sm"
              onClick={() => { navigator.clipboard?.writeText(s.id); }}
            >
              Copy ID
            </button>
            <button class="btn btn-icon btn-secondary" onClick={onClose}>
              <IconX />
            </button>
          </div>
        </div>

        {/* Timeline band */}
        <TimelineBand turns={turns} />

        {/* Key metrics strip */}
        <div class="detail-metrics">
          <div class="detail-metric">
            <span class="detail-metric-label">Duration</span>
            <span class="detail-metric-value">
              {s.duration_ms ? formatDuration(s.duration_ms) : formatDurationStr(s.duration)}
            </span>
          </div>
          <div class="detail-metric">
            <span class="detail-metric-label">Requests</span>
            <span class="detail-metric-value">{s.request_count}</span>
          </div>
          <div class="detail-metric">
            <span class="detail-metric-label">Messages</span>
            <span class="detail-metric-value">{s.message_count || 0}</span>
          </div>
          <div class="detail-metric">
            <span class="detail-metric-label">Tools</span>
            <span class="detail-metric-value">{totalTools}</span>
          </div>
          <div class="detail-metric">
            <span class="detail-metric-label">Bytes</span>
            <span class="detail-metric-value">{formatBytes(s.bytes_in)} / {formatBytes(s.bytes_out)}</span>
          </div>
          <div class="detail-metric">
            <span class="detail-metric-label">Tokens</span>
            <span class="detail-metric-value">
              {(s.tokens_in || 0).toLocaleString()} / {(s.tokens_out || 0).toLocaleString()}
            </span>
          </div>
        </div>

        {/* Main content */}
        <div class="detail-body">

          {/* Conversation stream — always visible */}
          <Section title="Conversation" defaultOpen={true} badge={turns ? turns.length : null}>
            {turns === null
              ? <div class="detail-loading">Loading\u2026</div>
              : <SessionTurns sessionId={session.id} preloadedTurns={turns} />
            }
          </Section>

          {/* Tool Use Summary */}
          {totalTools > 0 && (
            <Section title="Tool Use" defaultOpen={false} badge={totalTools}>
              <div class="detail-tool-grid">
                {toolEntries.map(([name, count]) => (
                  <div key={name} class="detail-tool-row">
                    <span class="mono">{name}</span>
                    <span class="detail-tool-count">{count}</span>
                  </div>
                ))}
              </div>
            </Section>
          )}

          {/* Risk Score Trend */}
          {riskCurve && riskCurve.length >= 2 && (
            <Section title="Risk Score" defaultOpen={true} badge={Math.round(riskScore)}>
              <RiskScoreChart points={riskCurve} />
            </Section>
          )}

          {/* Policy Violations */}
          {violations.length > 0 && (
            <Section title="Policy Violations" defaultOpen={true} badge={violations.length}>
              <div class="detail-violations">
                {violations.map((v, i) => (
                  <div key={i} class="detail-violation">
                    <div class="detail-violation-header">
                      <SeverityBadge severity={v.severity} />
                      <strong>{v.rule_name}</strong>
                      <span class="detail-violation-time">
                        {v.timestamp ? new Date(v.timestamp).toLocaleTimeString() : ''}
                      </span>
                    </div>
                    <div class="detail-violation-desc">{v.description}</div>
                    {v.matched_text && (
                      <div class="detail-violation-match">
                        <code>{v.matched_text}</code>
                      </div>
                    )}
                    {v.framework_ref && (
                      <span class="detail-violation-ref">{v.framework_ref}</span>
                    )}
                  </div>
                ))}
              </div>
            </Section>
          )}

          {/* Backend Routing */}
          {backendEntries.length > 0 && (
            <Section title="Backend Routing" defaultOpen={failedBackends.length > 0} badge={backendEntries.length}>
              <div class="detail-backend-grid">
                {backendEntries.map(([name, count]) => (
                  <div key={name} class="detail-backend-row">
                    <span class="mono">{name}</span>
                    <span class="detail-backend-count">{count} req</span>
                    {failedBackends.includes(name) && (
                      <span class="detail-backend-failed">failed</span>
                    )}
                  </div>
                ))}
              </div>
              {failedBackends.length > 0 && (
                <div class="detail-failover-note">
                  Failover occurred: {failedBackends.join(', ')} \u2192 {s.backend}
                </div>
              )}
            </Section>
          )}

          {/* Metadata */}
          {s.metadata && Object.keys(s.metadata).length > 0 && (
            <Section title="Metadata" defaultOpen={false} badge={Object.keys(s.metadata).length}>
              <div class="detail-metadata">
                {Object.entries(s.metadata).map(([k, v]) => (
                  <div key={k} class="detail-metadata-row">
                    <span class="detail-metadata-key">{k}</span>
                    <span class="detail-metadata-val mono">{v}</span>
                  </div>
                ))}
              </div>
            </Section>
          )}
        </div>
      </div>
    </div>
  )
}
