// Semantic evidence: the per-decision records, the correlation matches with
// their contributing events linked to the rows they name, and a collapsed
// shadow subsection. Every value shown is an identifier, enum, score or
// time from the API; the API carries no request content here.

import { useState } from 'preact/hooks'
import { SeverityBadge } from './shared/Badge'

export function correlationMatches(violations) {
  return (violations || []).filter((v) => v && v.correlation)
}

// anchorId derives the DOM id for a record from its event ID. A record with
// no event ID gets no id at all (undefined), never a shared 'evidence-undefined'
// that several records would collide on.
export function anchorId(eventId) {
  return eventId ? 'evidence-' + eventId : undefined
}

// anchoredViolations returns the event IDs a host must put on its ordinary
// violation rows (id={anchorId(event_id)}): violations that carry an event
// ID and are not already rendered inside SemanticEvidence as a decision row
// or a correlation match row. Hosts and linkedEvents agree on this set, so
// every resolved link has exactly one rendered target and no ID is emitted
// twice.
export function anchoredViolations(violations, decisions) {
  const decided = new Set((decisions || []).map((d) => d.event_id))
  const out = new Set()
  for (const v of violations || []) {
    if (v && v.event_id && !v.correlation && !decided.has(v.event_id)) out.add(v.event_id)
  }
  return out
}

// linkedEvents resolves a match's contributing event IDs: a semantic
// decision record (kind decision), another rendered violation (kind
// violation), or nothing this record holds (kind unresolved: an evicted
// decision record, a tool event or a regex event not present in violations).
// anchored is the set of ordinary violation IDs the host actually renders
// as anchors; it defaults to anchoredViolations(violations, decisions).
export function linkedEvents(match, decisions, violations, anchored) {
  const byDecision = new Map((decisions || []).filter((d) => d && d.event_id).map((d) => [d.event_id, d]))
  const rendered = anchored || anchoredViolations(violations, decisions)
  const byViolation = new Map()
  for (const v of violations || []) {
    if (!v || !v.event_id || v === match) continue
    if (v.correlation || rendered.has(v.event_id)) byViolation.set(v.event_id, v)
  }
  return (match.correlation.event_ids || []).map((id) => {
    if (!id) return { id, kind: 'unresolved', ref: null }
    if (byDecision.has(id)) return { id, kind: 'decision', ref: byDecision.get(id) }
    if (byViolation.has(id)) return { id, kind: 'violation', ref: byViolation.get(id) }
    return { id, kind: 'unresolved', ref: null }
  })
}

export function evidenceMarkers({ decisions, violations, semanticShadow, correlationShadow }) {
  const out = []
  for (const d of decisions || []) {
    out.push({ type: 'semantic_decision', timestamp: d.timestamp, title: `${d.rule_name} ${fmtProb(d.evidence && d.evidence.probability)}` })
  }
  for (const m of correlationMatches(violations)) {
    out.push({ type: 'correlation_match', timestamp: m.timestamp, title: `${m.correlation.rule} (${(m.correlation.event_ids || []).length} events)` })
  }
  for (const s of semanticShadow || []) {
    out.push({ type: 'shadow_decision', timestamp: s.timestamp, title: `shadow ${s.signal} ${fmtProb(s.probability)}` })
  }
  for (const s of correlationShadow || []) {
    out.push({ type: 'shadow_match', timestamp: s.timestamp, title: `shadow ${s.rule} (${(s.event_ids || []).length} events)` })
  }
  return out
}

function fmtProb(p) {
  return typeof p === 'number' ? p.toFixed(2) : ''
}

function fmtTime(ts) {
  return ts ? new Date(ts).toLocaleTimeString() : ''
}

function DecisionRow({ d }) {
  const ev = d.evidence || {}
  return (
    <div class="evidence-decision" id={anchorId(d.event_id)}>
      <div class="evidence-row-header">
        <SeverityBadge severity={d.severity} />
        {d.effective_severity && d.effective_severity !== d.severity && <span class="evidence-effective">effective {d.effective_severity}</span>}
        <strong>{d.rule_name}</strong>
        {d.evidence_only && <span class="evidence-badge">evidence only</span>}
        {ev.vetoed && <span class="evidence-badge evidence-veto">vetoed</span>}
        <span class="evidence-time">{fmtTime(d.timestamp)}</span>
      </div>
      <div class="evidence-row-meta mono">
        {ev.signal} {fmtProb(ev.probability)} · {d.source_role} #{d.message_index}
        {d.would_contribute_points ? ` · would add ${d.would_contribute_points}` : ''}
        {' · '}{ev.decision_id}
      </div>
    </div>
  )
}

function MatchRow({ m, decisions, violations, anchored }) {
  const c = m.correlation
  const links = linkedEvents(m, decisions, violations, anchored)
  return (
    <div class="evidence-match" id={anchorId(m.event_id)}>
      <div class="evidence-row-header">
        <SeverityBadge severity={m.severity} />
        <strong>{c.rule}</strong>
        <span class="evidence-badge">{c.shape} · {c.mode}</span>
        {m.evidence_only && <span class="evidence-badge">evidence only</span>}
        <span class="evidence-time">{fmtTime(m.timestamp)}</span>
      </div>
      <div class="evidence-row-meta mono">{c.match_id}</div>
      <ul class="evidence-events">
        {links.map((l) => (
          <li key={l.id}>
            {l.kind === 'unresolved'
              ? <span class="evidence-unresolved mono" title="not among this record's decisions or violations">{l.id}</span>
              : <a class="evidence-link mono" href={'#' + anchorId(l.id)}>{l.id}</a>}
            {l.kind === 'decision' && <span class="evidence-link-label"> {l.ref.rule_name} #{l.ref.message_index}</span>}
            {l.kind === 'violation' && <span class="evidence-link-label"> {l.ref.rule_name}</span>}
          </li>
        ))}
      </ul>
    </div>
  )
}

function ShadowSection({ semanticShadow, correlationShadow }) {
  const [open, setOpen] = useState(false)
  const n = (semanticShadow || []).length + (correlationShadow || []).length
  if (n === 0) return null
  return (
    <div class="evidence-shadow">
      <button class="evidence-shadow-toggle" onClick={() => setOpen(!open)}>
        {open ? '▾' : '▸'} Shadow ({n})
      </button>
      {open && (
        <div class="evidence-shadow-body">
          {(correlationShadow || []).map((s, i) => (
            <div key={'m' + i} class="evidence-row-meta mono">
              shadow match {s.rule} · {s.shape} · {(s.event_ids || []).length} events · {s.match_id} · {fmtTime(s.timestamp)}
            </div>
          ))}
          {(semanticShadow || []).map((s, i) => (
            <div key={'d' + i} class="evidence-row-meta mono">
              shadow {s.signal} {fmtProb(s.probability)}{s.vetoed ? ' vetoed' : ''} · {s.source_role} #{s.message_index} · {s.decision_id} · {fmtTime(s.timestamp)}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

// anchored (optional) is the set of ordinary violation event IDs the host
// renders with id={anchorId(event_id)}; see anchoredViolations.
export function SemanticEvidence({ decisions, violations, semanticShadow, correlationShadow, anchored }) {
  const matches = correlationMatches(violations)
  const ds = decisions || []
  const shadowCount = (semanticShadow || []).length + (correlationShadow || []).length
  if (ds.length === 0 && matches.length === 0 && shadowCount === 0) return null
  return (
    <div class="evidence">
      {matches.length > 0 && (
        <div class="evidence-group">
          <div class="evidence-group-title">Correlation matches</div>
          {matches.map((m, i) => <MatchRow key={i} m={m} decisions={ds} violations={violations} anchored={anchored} />)}
        </div>
      )}
      {ds.length > 0 && (
        <div class="evidence-group">
          <div class="evidence-group-title">Semantic decisions</div>
          {ds.map((d, i) => <DecisionRow key={i} d={d} />)}
        </div>
      )}
      <ShadowSection semanticShadow={semanticShadow} correlationShadow={correlationShadow} />
    </div>
  )
}
