import { describe, it, expect, afterEach } from 'vitest'
import { render, cleanup, fireEvent } from '@testing-library/preact'
import { SemanticEvidence, correlationMatches, linkedEvents, evidenceMarkers } from './SemanticEvidence'
import fixture from './__fixtures__/history-correlation.json'

const props = {
  decisions: fixture.semantic_decisions,
  violations: fixture.violations,
  semanticShadow: fixture.semantic_shadow,
  correlationShadow: fixture.correlation_shadow,
}

describe('SemanticEvidence', () => {
  afterEach(() => cleanup())

  it('lists every decision with rule, severities, evidence badge, probability, signal, index, decision ID and veto', () => {
    const { container } = render(<SemanticEvidence {...props} />)
    const rows = container.querySelectorAll('.evidence-decision')
    expect(rows.length).toBe(3)
    const text = rows[1].textContent
    expect(text).toContain('injection_elevated')
    expect(text).toContain('info')
    expect(text).toContain('evidence')
    expect(text).toContain('0.41')
    expect(text).toContain('injection')
    expect(text).toContain('#4')
    expect(text).toContain('dec_0000000000000000000000000000000b')
    expect(text).not.toContain('vetoed')
  })

  it('links a match to the decision rows its event IDs name', () => {
    const m = correlationMatches(fixture.violations)
    expect(m.length).toBe(1)
    const links = linkedEvents(m[0], fixture.semantic_decisions, fixture.violations)
    expect(links.map((l) => l.kind)).toEqual(['decision', 'decision', 'decision'])
    const { container } = render(<SemanticEvidence {...props} />)
    const match = container.querySelector('.evidence-match')
    expect(match.textContent).toContain('slow_burn_injection')
    expect(match.textContent).toContain('audit')
    expect(match.textContent).toContain('cm_0123456789abcdef0123456789abcdef')
    const anchors = match.querySelectorAll('a.evidence-link')
    expect(anchors.length).toBe(3)
    expect(anchors[0].getAttribute('href')).toBe('#evidence-ev_0000000000000000000000000000000a')
    expect(container.querySelector('#evidence-ev_0000000000000000000000000000000a')).toBeTruthy()
  })

  // Review Focus 5: IDs that resolve to nothing (evicted records, tool
  // events, regex events) render as unresolved, never crash or hide the match.
  it('renders unresolved contributing IDs without crashing', () => {
    const violations = JSON.parse(JSON.stringify(fixture.violations))
    violations[1].correlation.event_ids = ['te_0000000000000000000000000000dead', 'ev_0000000000000000000000000000000a']
    const links = linkedEvents(correlationMatches(violations)[0], [], violations)
    expect(links.map((l) => l.kind)).toEqual(['unresolved', 'unresolved'])
    const { container } = render(<SemanticEvidence decisions={[]} violations={violations} />)
    const match = container.querySelector('.evidence-match')
    expect(match).toBeTruthy()
    expect(match.querySelectorAll('.evidence-unresolved').length).toBe(2)
    expect(match.textContent).toContain('te_0000000000000000000000000000dead')
  })

  it('links a match to a non-semantic violation by event_id', () => {
    const violations = [
      { rule_name: 'prompt_injection_probe', event_id: 'ev_regex', event_category: 'prompt_injection', severity: 'warning', timestamp: '2026-10-20T09:01:00Z' },
      { rule_name: 'correlation.probe_twice', event_id: 'cm_1', event_category: 'correlation_match', evidence_only: true, timestamp: '2026-10-20T09:02:00Z',
        correlation: { match_id: 'cm_1', rule: 'probe_twice', shape: 'repeated', mode: 'audit', event_ids: ['ev_regex'], categories: ['prompt_injection'], first_event: '2026-10-20T09:01:00Z', last_event: '2026-10-20T09:02:00Z' } },
    ]
    const links = linkedEvents(correlationMatches(violations)[0], [], violations)
    expect(links[0].kind).toBe('violation')
    expect(links[0].ref.rule_name).toBe('prompt_injection_probe')
  })

  it('keeps shadow data in a collapsed subsection', () => {
    const { container } = render(<SemanticEvidence {...props} />)
    expect(container.querySelector('.evidence-shadow-body')).toBeNull()
    fireEvent.click(container.querySelector('.evidence-shadow-toggle'))
    const body = container.querySelector('.evidence-shadow-body')
    expect(body.textContent).toContain('dec_000000000000000000000000000000ff')
    expect(body.textContent).toContain('cm_fedcba9876543210fedcba9876543210')
    expect(body.textContent).toContain('0.12')
  })

  it('builds timeline markers for decisions, matches and shadow entries', () => {
    const markers = evidenceMarkers(props)
    expect(markers.map((m) => m.type).sort()).toEqual(['correlation_match', 'semantic_decision', 'semantic_decision', 'semantic_decision', 'shadow_decision', 'shadow_match'])
    expect(markers.find((m) => m.type === 'correlation_match').title).toContain('3 events')
  })

  it('renders nothing when there is no evidence', () => {
    const { container } = render(<SemanticEvidence decisions={[]} violations={[]} />)
    expect(container.textContent).toBe('')
  })

  it('emits no anchor id for records without an event_id, so ids never collide', () => {
    const mk = (n) => ({ ...fixture.semantic_decisions[0], event_id: undefined, evidence: { ...fixture.semantic_decisions[0].evidence, decision_id: 'dec_' + n } })
    const match = { ...fixture.violations[1], event_id: undefined, correlation: { ...fixture.violations[1].correlation, event_ids: [''] } }
    const { container } = render(<SemanticEvidence decisions={[mk(1), mk(2)]} violations={[match]} />)
    expect(container.querySelectorAll('.evidence-decision').length).toBe(2)
    const ids = [...container.querySelectorAll('[id]')].map((e) => e.id)
    expect(ids.length).toBe(new Set(ids).size)
    expect(ids.some((x) => x.includes('undefined'))).toBe(false)
    // A link to a record with no ID renders as unresolved, not as an href.
    expect(container.querySelector('a.evidence-link')).toBeNull()
    expect(container.querySelector('.evidence-unresolved')).toBeTruthy()
  })
})
