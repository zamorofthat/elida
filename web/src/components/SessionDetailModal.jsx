import { useState, useEffect, useRef } from 'preact/hooks'
import { apiFetch } from '../apiFetch'
import { formatBytes, formatDuration, formatDurationStr, truncateId } from '../utils'
import { StateBadge, SeverityBadge, FingerprintBadge } from './shared/Badge'
import { IconX } from './shared/Icons'
import { SessionTurns } from './SessionTurns'
import { TimelineBand } from './TimelineBand'
import { SemanticEvidence, evidenceMarkers, anchorId, anchoredViolations } from './SemanticEvidence'

// ---------------------------------------------------------------------------
// Expandable matched text for policy violations
// ---------------------------------------------------------------------------

function ExpandableMatch({ text }) {
  const [expanded, setExpanded] = useState(false)
  const isLong = text.length > 80

  return (
    <div
      class={`detail-violation-match ${isLong ? 'expandable' : ''}`}
      onClick={isLong ? () => setExpanded(!expanded) : undefined}
    >
      <code class={expanded ? 'expanded' : ''}>
        {expanded || !isLong ? text : text.slice(0, 80) + '…'}
      </code>
      {isLong && (
        <span class="expand-hint">{expanded ? '▲ collapse' : '▼ expand'}</span>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// Behavior Radar — 7-axis radar chart of behavioral features
// ---------------------------------------------------------------------------

const FEATURE_LABELS = {
  turn_count: 'Turns',
  tool_call_ratio: 'Tool Ratio',
  tool_call_entropy: 'Tool Diversity',
  cadence_median: 'Cadence',
  cadence_cv: 'Irregularity',
  token_ratio: 'Token Balance',
  backend_continuity: 'Backend Stability',
}

function BehaviorRadar({ features }) {
  if (!features || features.length === 0) return null

  const CX = 100, CY = 100, R = 70
  const n = features.length

  // Normalize values to 0-1 range for display
  // Use log scaling for large values, cap at reasonable max
  const maxVals = [5, 1, 3, 10, 3, 3, 1] // reasonable max per feature
  const normalized = features.map((f, i) => {
    const max = maxVals[i] || 1
    return Math.min(f.value / max, 1)
  })

  const angle = (i) => (Math.PI * 2 * i) / n - Math.PI / 2
  const px = (i, r) => CX + Math.cos(angle(i)) * r
  const py = (i, r) => CY + Math.sin(angle(i)) * r

  // Grid rings
  const rings = [0.25, 0.5, 0.75, 1.0]

  // Data polygon
  const dataPoints = normalized.map((v, i) => `${px(i, v * R).toFixed(1)},${py(i, v * R).toFixed(1)}`).join(' ')

  // Axis lines
  const axes = features.map((_, i) => ({
    x2: px(i, R),
    y2: py(i, R),
    labelX: px(i, R + 14),
    labelY: py(i, R + 14),
    label: FEATURE_LABELS[features[i].name] || features[i].name,
  }))

  return (
    <svg viewBox="0 0 200 200" class="behavior-radar">
      {/* Grid rings */}
      {rings.map((r, i) => (
        <polygon
          key={i}
          points={features.map((_, j) => `${px(j, r * R).toFixed(1)},${py(j, r * R).toFixed(1)}`).join(' ')}
          fill="none" stroke="var(--border-color)" stroke-width="0.5"
        />
      ))}

      {/* Axis lines */}
      {axes.map((a, i) => (
        <line key={i} x1={CX} y1={CY} x2={a.x2} y2={a.y2}
          stroke="var(--border-color)" stroke-width="0.5" />
      ))}

      {/* Data polygon */}
      <polygon
        points={dataPoints}
        fill="rgba(99, 102, 241, 0.2)"
        stroke="#6366f1"
        stroke-width="1.5"
      />

      {/* Data points */}
      {normalized.map((v, i) => (
        <circle key={i} cx={px(i, v * R)} cy={py(i, v * R)} r="2.5"
          fill="#6366f1" />
      ))}

      {/* Labels */}
      {axes.map((a, i) => (
        <text key={i} x={a.labelX} y={a.labelY + 3}
          text-anchor="middle" fill="var(--text-dim)" font-size="7"
          font-family="'SF Mono', monospace">
          {a.label}
        </text>
      ))}
    </svg>
  )
}

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
// Session Detail Modal
// ---------------------------------------------------------------------------

const POLL_MS = 3000

// A session that has just ended may not be in history yet: the store writes
// the row after the session closes. A 404 is retried on this many polls
// before the record is shown as unavailable.
const HISTORY_RETRIES = 5

export function SessionDetailModal({ session, onClose, onKill }) {
  const [turns, setTurns] = useState(null)
  const [flagged, setFlagged] = useState(null)
  const [detail, setDetail] = useState(null)
  // historyDetail is populated only by the successful final history fetch;
  // historyStatus is '' | 'pending' | 'unavailable'.
  const [historyDetail, setHistoryDetail] = useState(null)
  const [historyStatus, setHistoryStatus] = useState('')
  const [riskCurve, setRiskCurve] = useState(null)
  const [behavior, setBehavior] = useState(null)
  const modalRef = useRef(null)

  // The modal's one poll. The first tick runs on open; later ticks run every
  // POLL_MS and refresh the live detail (shadow lists), the flagged evidence
  // and the turns together. When the live detail reports a final state or
  // 404s, the poll switches to /control/history/{id}: that record becomes
  // the authoritative evidence source (an empty list there replaces stale
  // live evidence) and polling stops once it is loaded or the bounded 404
  // retries are spent. Other history errors stop polling with an
  // unavailable state; there is no fallback. Every response is checked
  // against the controller (ID change or unmount aborts it) and a
  // per-resource sequence number, so a slow earlier response never replaces
  // a newer one.
  useEffect(() => {
    if (!session?.id) return
    const id = session.id
    const controller = new AbortController()
    setTurns(null)
    setFlagged(null)
    setDetail(null)
    setHistoryDetail(null)
    setHistoryStatus('')

    let phase = 'live' // 'live' | 'history' | 'done'
    let historyTries = 0
    let seq = 0
    const applied = { detail: 0, flagged: 0, turns: 0 }
    let timer = null

    const get = (path) => apiFetch(path, { signal: controller.signal })
    const stop = () => { phase = 'done'; clearInterval(timer) }
    // newest applies fn only for the latest response of its kind.
    const newest = (kind, n, fn) => {
      if (controller.signal.aborted || n < applied[kind]) return
      applied[kind] = n
      fn()
    }

    // At most one history request is in flight: a tick that lands while one
    // is pending is skipped, so attempts never overlap and only results, not
    // dispatches, count toward the retry budget. settleHistory also ignores
    // a result that arrives once the history phase is over (the record was
    // loaded or the retries were spent), so it can never overwrite a loaded
    // record or its status.
    let historyInFlight = false
    const settleHistory = (record, status) => {
      historyInFlight = false
      if (controller.signal.aborted || phase !== 'history') return
      if (record) {
        setHistoryDetail(record)
        setHistoryStatus('')
        stop()
        return
      }
      historyTries++
      if (status === 404 && historyTries < HISTORY_RETRIES) {
        setHistoryStatus('pending')
        return
      }
      setHistoryStatus('unavailable')
      stop()
    }
    const fetchHistory = () => {
      if (historyInFlight) return
      historyInFlight = true
      get('/control/history/' + id)
        .then(res => (res.ok
          ? res.json().then(record => settleHistory(record, res.status))
          : settleHistory(null, res.status)))
        .catch(() => settleHistory(null, 0))
    }

    const toHistory = () => {
      if (phase !== 'live') return
      phase = 'history'
      setHistoryStatus('pending')
      fetchHistory()
    }

    const pollLive = () => {
      const d = ++seq
      get('/control/sessions/' + id)
        .then(res => {
          if (controller.signal.aborted) return
          if (res.status === 404) { toHistory(); return }
          if (!res.ok) return
          return res.json().then(data => newest('detail', d, () => {
            setDetail(data)
            if (data.state !== 'active') toHistory()
          }))
        })
        .catch(() => {})

      const f = ++seq
      get('/control/flagged/' + id)
        .then(res => res.ok ? res.json() : null)
        .then(data => { if (data) newest('flagged', f, () => setFlagged(data)) })
        .catch(() => {})

      const t = ++seq
      get('/control/sessions/' + id + '/turns?full=true')
        .then(res => res.ok ? res.json() : null)
        .then(data => newest('turns', t, () => setTurns(prev => data ? (data.turns || []) : (prev || []))))
        .catch(() => { newest('turns', t, () => setTurns(prev => prev || [])) })
    }

    pollLive()
    timer = setInterval(() => {
      if (document.hidden) return
      if (phase === 'live') pollLive()
      else if (phase === 'history') fetchHistory()
    }, POLL_MS)

    return () => { clearInterval(timer); controller.abort() }
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

  // Fetch behavioral features
  useEffect(() => {
    if (!session?.id) return
    const controller = new AbortController()

    apiFetch('/control/sessions/' + session.id + '/behavior', { signal: controller.signal })
      .then(res => res.ok ? res.json() : null)
      .then(data => { if (!controller.signal.aborted) setBehavior(data) })
      .catch(() => {})

    return () => controller.abort()
  }, [session?.id])

  // Escape key
  useEffect(() => {
    const onKey = (e) => { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  if (!session) return null

  const s = detail || session
  // The persisted record, once loaded, is the authoritative source for the
  // violations, the decision records and the shadow lists; before that the
  // flagged evidence and the live detail are.
  const source = historyDetail || flagged || detail
  const violations = source?.violations || []
  const decisions = source?.semantic_decisions || []
  const semanticShadow = (historyDetail || detail)?.semantic_shadow || []
  const correlationShadow = (historyDetail || detail)?.correlation_shadow || []
  const anchored = anchoredViolations(violations, decisions)
  const markers = evidenceMarkers({ decisions, violations, semanticShadow, correlationShadow })
  const evidenceCount = decisions.length + violations.filter(v => v.correlation).length
  const showEvidence = evidenceCount > 0 || semanticShadow.length > 0 || correlationShadow.length > 0 || historyStatus !== ''
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
            <FingerprintBadge bucket={s.fingerprint_bucket} distance={s.fingerprint_distance} />
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
        <TimelineBand turns={turns} markers={markers} />

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
                  <div key={i} class="detail-violation" id={anchored.has(v.event_id) ? anchorId(v.event_id) : undefined}>
                    <div class="detail-violation-header">
                      <SeverityBadge severity={v.severity} />
                      <strong>{v.rule_name}</strong>
                      <span class="detail-violation-time">
                        {v.timestamp ? new Date(v.timestamp).toLocaleTimeString() : ''}
                      </span>
                    </div>
                    <div class="detail-violation-desc">{v.description}</div>
                    {v.matched_text && (
                      <ExpandableMatch text={v.matched_text} />
                    )}
                    {v.framework_ref && (
                      <span class="detail-violation-ref">{v.framework_ref}</span>
                    )}
                  </div>
                ))}
              </div>
            </Section>
          )}

          {/* Semantic evidence: decision records, correlation matches, shadow */}
          {showEvidence && (
            <Section title="Semantic evidence" defaultOpen={true} badge={evidenceCount}>
              {historyStatus === 'pending' && (
                <div class="evidence-pending">Loading the persisted record…</div>
              )}
              {historyStatus === 'unavailable' && (
                <div class="evidence-unavailable">Persisted record unavailable; showing the last live evidence.</div>
              )}
              <SemanticEvidence
                decisions={decisions}
                violations={violations}
                semanticShadow={semanticShadow}
                correlationShadow={correlationShadow}
                anchored={anchored}
              />
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

          {/* Behavioral Fingerprint */}
          {behavior && behavior.features && behavior.features.length > 0 && (
            <Section
              title="Behavior"
              defaultOpen={false}
              badge={
                <>
                  {behavior.class}
                  <FingerprintBadge bucket={s.fingerprint_bucket} distance={s.fingerprint_distance} />
                </>
              }
            >
              <div class="behavior-container">
                <BehaviorRadar features={behavior.features} />
                <div class="behavior-features">
                  {behavior.features.map((f, i) => (
                    <div key={i} class="behavior-feature-row">
                      <span class="behavior-feature-name">
                        {FEATURE_LABELS[f.name] || f.name}
                      </span>
                      <span class="behavior-feature-value mono">
                        {typeof f.value === 'number' ? f.value.toFixed(2) : f.value}
                      </span>
                    </div>
                  ))}
                </div>
              </div>
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
