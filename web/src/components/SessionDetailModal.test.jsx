import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, cleanup, act, fireEvent } from '@testing-library/preact'
import { SessionDetailModal } from './SessionDetailModal'
import { AUTH_KEY } from '../apiFetch'
import fixture from './__fixtures__/history-correlation.json'

// The modal's evidence lifecycle: live evidence refreshes on the modal's one
// 3-second poll; when the live detail reports a final state or 404s the modal
// switches to the persisted record, which is authoritative even when its
// lists are empty; a temporarily missing history row is retried a bounded
// number of times; ID changes abort in-flight requests and clear state; a
// slow earlier response never overwrites a newer one.

const SID = 'sess-live-1'
const POLL = 3000
// More ticks than the modal's bounded history retries, so an overlapping
// dispatch or a dispatch-counted budget would show up as 'unavailable'.
const HISTORY_RETRY_PROBE = 8

function json(body, status = 200) {
  return { status, ok: status < 400, json: () => Promise.resolve(body) }
}

function deferred() {
  let resolve
  const promise = new Promise((r) => { resolve = r })
  return { promise, resolve }
}

const decisionA = fixture.semantic_decisions[0]
const matchRow = fixture.violations[1]
const shadowMatch = fixture.correlation_shadow[0]

describe('SessionDetailModal evidence lifecycle', () => {
  let routes
  let mockFetch
  let calls

  beforeEach(() => {
    vi.useFakeTimers()
    const storage = { [AUTH_KEY]: 'k' }
    vi.stubGlobal('localStorage', {
      getItem: vi.fn((k) => storage[k] ?? null),
      setItem: vi.fn((k, v) => { storage[k] = String(v) }),
      removeItem: vi.fn((k) => { delete storage[k] }),
    })
    routes = {}
    calls = []
    mockFetch = vi.fn((url, opts = {}) => {
      calls.push({ url, opts })
      const r = routes[url]
      if (!r) return Promise.resolve(json({}, 404))
      const v = typeof r === 'function' ? r(opts) : r
      return v && typeof v.then === 'function' ? v : Promise.resolve(v)
    })
    vi.stubGlobal('fetch', mockFetch)
  })

  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
    vi.useRealTimers()
  })

  // Let the mocked fetch promise chains settle without advancing the clock.
  async function flush() {
    await act(async () => {
      for (let i = 0; i < 8; i++) await Promise.resolve()
    })
  }

  async function tick(ms = POLL) {
    await act(async () => { await vi.advanceTimersByTimeAsync(ms) })
    await flush()
  }

  function count(url) {
    return calls.filter((c) => c.url === url).length
  }

  function live(id, extra = {}) {
    return json({ id, state: 'active', request_count: 1, ...extra })
  }

  function open(session = { id: SID, state: 'active' }) {
    return render(<SessionDetailModal session={session} onClose={() => {}} />)
  }

  it('renders live evidence from the poll and refreshes it on the existing cadence', async () => {
    routes['/control/sessions/' + SID] = () => live(SID)
    const r = open()
    await flush()
    expect(r.container.querySelector('.evidence')).toBeNull()
    expect(count('/control/sessions/' + SID)).toBe(1)
    expect(count('/control/flagged/' + SID)).toBe(1)

    // The next poll delivers a decision, a match and a shadow match.
    routes['/control/flagged/' + SID] = () => json({ violations: [matchRow], semantic_decisions: [decisionA] })
    routes['/control/sessions/' + SID] = () => live(SID, { correlation_shadow: [shadowMatch] })
    await tick()

    // The shadow list grew, which counts as a change: evidence refetched.
    expect(count('/control/sessions/' + SID)).toBe(2)
    expect(count('/control/flagged/' + SID)).toBe(2)
    expect(count('/control/sessions/' + SID + '/turns?full=true')).toBe(2)
    expect(r.container.querySelectorAll('.evidence-decision').length).toBe(1)
    expect(r.container.querySelectorAll('.evidence-match').length).toBe(1)
    expect(r.container.querySelector('.timeline-dot-semantic_decision')).toBeTruthy()
    expect(r.container.querySelector('.timeline-dot-correlation_match')).toBeTruthy()
    expect(r.container.querySelector('.timeline-dot-shadow_match')).toBeTruthy()

    // One poll, not two: one more interval means exactly one more live
    // detail fetch; nothing changed, so the expensive endpoints are skipped.
    await tick()
    expect(count('/control/sessions/' + SID)).toBe(3)
    expect(count('/control/flagged/' + SID)).toBe(2)
    expect(count('/control/history/' + SID)).toBe(0)
  })

  it('switches to the persisted record when the session completes, replacing live evidence including empty lists', async () => {
    routes['/control/sessions/' + SID] = () => live(SID, { correlation_shadow: [shadowMatch] })
    routes['/control/flagged/' + SID] = () => json({ violations: [matchRow], semantic_decisions: [decisionA] })
    const r = open()
    await flush()
    expect(r.container.querySelectorAll('.evidence-decision').length).toBe(1)
    expect(r.container.querySelector('.evidence-shadow-toggle')).toBeTruthy()

    // The session ends: the persisted record has the match but no decision
    // records and no shadow entries, and that empty state must win.
    routes['/control/sessions/' + SID] = () => json({ id: SID, state: 'completed' })
    routes['/control/history/' + SID] = () => json({ id: SID, state: 'completed', violations: [matchRow] })
    await tick()

    expect(count('/control/history/' + SID)).toBe(1)
    expect(r.container.querySelectorAll('.evidence-decision').length).toBe(0)
    expect(r.container.querySelectorAll('.evidence-match').length).toBe(1)
    expect(r.container.querySelectorAll('.evidence-unresolved').length).toBe(3)
    expect(r.container.querySelector('.evidence-shadow-toggle')).toBeNull()
    expect(r.container.querySelector('.evidence-unavailable')).toBeNull()

    // Polling stops once the persisted record is loaded.
    const sessionsBefore = count('/control/sessions/' + SID)
    const flaggedBefore = count('/control/flagged/' + SID)
    await tick()
    await tick()
    expect(count('/control/sessions/' + SID)).toBe(sessionsBefore)
    expect(count('/control/flagged/' + SID)).toBe(flaggedBefore)
    expect(count('/control/history/' + SID)).toBe(1)
  })

  it('switches to the persisted record on a live-detail 404 and renders it with resolved links', async () => {
    routes['/control/history/' + fixture.id] = () => json(fixture)
    const r = open({ id: fixture.id, state: 'completed' })
    await flush()

    expect(count('/control/history/' + fixture.id)).toBe(1)
    expect(r.container.querySelectorAll('.evidence-decision').length).toBe(3)
    expect(r.container.querySelectorAll('.evidence-match').length).toBe(1)
    // History violations drive the Policy Violations section too.
    expect(r.container.querySelectorAll('.detail-violation').length).toBe(2)

    // Every link from the match resolves to exactly one rendered target, and
    // the semantic violation row does not duplicate its decision row's ID.
    const links = r.container.querySelectorAll('a.evidence-link')
    expect(links.length).toBe(3)
    for (const a of links) {
      const id = a.getAttribute('href').slice(1)
      expect(r.container.querySelectorAll('[id="' + id + '"]').length).toBe(1)
    }
    const target = r.container.querySelector(links[0].getAttribute('href'))
    expect(target.className).toContain('evidence-decision')
    fireEvent.click(links[0])
    expect(r.container.querySelectorAll('[id="evidence-ev_0000000000000000000000000000000c"]').length).toBe(1)
    expect(r.container.querySelector('.detail-violation[id]')).toBeNull()

    await tick()
    expect(count('/control/history/' + fixture.id)).toBe(1)
    expect(count('/control/sessions/' + fixture.id)).toBe(1)
  })

  it('anchors a non-semantic violation row so a match can link to it', async () => {
    const regex = { rule_name: 'prompt_injection_probe', event_id: 'ev_regex', event_category: 'prompt_injection', severity: 'warning', timestamp: '2026-10-20T09:01:00Z' }
    const match = { rule_name: 'correlation.probe_twice', event_id: 'cm_1', event_category: 'correlation_match', severity: 'warning', evidence_only: true, timestamp: '2026-10-20T09:02:00Z',
      correlation: { match_id: 'cm_1', rule: 'probe_twice', shape: 'repeated', mode: 'audit', event_ids: ['ev_regex'], categories: ['prompt_injection'], first_event: '2026-10-20T09:01:00Z', last_event: '2026-10-20T09:02:00Z' } }
    routes['/control/history/' + SID] = () => json({ id: SID, state: 'completed', violations: [regex, match] })
    const r = open({ id: SID, state: 'completed' })
    await flush()

    const link = r.container.querySelector('a.evidence-link')
    expect(link.getAttribute('href')).toBe('#evidence-ev_regex')
    const targets = r.container.querySelectorAll('[id="evidence-ev_regex"]')
    expect(targets.length).toBe(1)
    expect(targets[0].className).toContain('detail-violation')
  })

  it('retries a temporarily missing history row on the cadence, then loads it', async () => {
    let historyHits = 0
    routes['/control/sessions/' + SID] = () => json({ id: SID, state: 'completed' })
    routes['/control/history/' + SID] = () => (++historyHits < 3 ? json({}, 404) : json(fixture))
    routes['/control/flagged/' + SID] = () => json({ violations: [matchRow] }) // the notices only qualify existing evidence
    const r = open()
    await flush()
    expect(historyHits).toBe(1)
    expect(r.container.querySelector('.evidence-pending')).toBeTruthy()
    await tick()
    expect(historyHits).toBe(2)
    expect(r.container.querySelectorAll('.evidence-decision').length).toBe(0)
    await tick()
    expect(historyHits).toBe(3)
    expect(r.container.querySelectorAll('.evidence-decision').length).toBe(3)
    expect(r.container.querySelector('.evidence-pending')).toBeNull()
    await tick()
    expect(historyHits).toBe(3)
  })

  it('shows an unavailable state after the bounded history retries and keeps the last live evidence', async () => {
    routes['/control/sessions/' + SID] = () => live(SID)
    routes['/control/flagged/' + SID] = () => json({ violations: [matchRow], semantic_decisions: [decisionA] })
    const r = open()
    await flush()
    expect(r.container.querySelectorAll('.evidence-decision').length).toBe(1)

    routes['/control/sessions/' + SID] = () => json({}, 404)
    for (let i = 0; i < 8; i++) await tick()

    const hits = count('/control/history/' + SID)
    expect(hits).toBeGreaterThan(1)
    expect(hits).toBeLessThan(8)
    expect(r.container.querySelector('.evidence-unavailable')).toBeTruthy()
    expect(r.container.querySelectorAll('.evidence-decision').length).toBe(1)
    await tick()
    expect(count('/control/history/' + SID)).toBe(hits)
  })

  it('never overlaps history attempts, counts results toward the retry budget and leaves a loaded record untouched', async () => {
    const slow = deferred()
    let historyHits = 0
    routes['/control/sessions/' + SID] = () => json({ id: SID, state: 'completed' })
    routes['/control/history/' + SID] = () => (++historyHits === 1 ? slow.promise : json(fixture))
    routes['/control/flagged/' + SID] = () => json({ violations: [matchRow] }) // the notices only qualify existing evidence
    const r = open()
    await flush()
    expect(historyHits).toBe(1)
    expect(r.container.querySelector('.evidence-pending')).toBeTruthy()

    // Ticks while the first attempt is still pending dispatch nothing and
    // spend no retries.
    for (let i = 0; i < HISTORY_RETRY_PROBE; i++) await tick()
    expect(historyHits).toBe(1)
    expect(r.container.querySelector('.evidence-unavailable')).toBeNull()

    // The slow attempt settles as a 404: one retry spent, next tick loads.
    slow.resolve(json({}, 404))
    await flush()
    expect(r.container.querySelector('.evidence-pending')).toBeTruthy()
    await tick()
    expect(historyHits).toBe(2)
    expect(r.container.querySelectorAll('.evidence-decision').length).toBe(3)
    expect(r.container.querySelector('.evidence-pending')).toBeNull()
    expect(r.container.querySelector('.evidence-unavailable')).toBeNull()

    // Once loaded, nothing later touches the record or its status.
    await tick()
    await tick()
    expect(historyHits).toBe(2)
    expect(r.container.querySelectorAll('.evidence-decision').length).toBe(3)
    expect(r.container.querySelector('.evidence-pending')).toBeNull()
    expect(r.container.querySelector('.evidence-unavailable')).toBeNull()
  })

  it('does not retry on a non-404 history error', async () => {
    routes['/control/sessions/' + SID] = () => json({}, 404)
    routes['/control/history/' + SID] = () => json({}, 503)
    routes['/control/flagged/' + SID] = () => json({ violations: [matchRow] }) // the notices only qualify existing evidence
    const r = open()
    await flush()
    expect(count('/control/history/' + SID)).toBe(1)
    expect(r.container.querySelector('.evidence-unavailable')).toBeTruthy()
    await tick()
    expect(count('/control/history/' + SID)).toBe(1)
  })

  it('clears evidence, aborts in-flight requests and ignores their late responses when the ID changes', async () => {
    const slow = deferred()
    routes['/control/sessions/' + SID] = () => live(SID)
    routes['/control/flagged/' + SID] = () => slow.promise
    const r = open()
    await flush()
    expect(r.container.querySelector('.evidence')).toBeNull()
    const first = calls.find((c) => c.url === '/control/flagged/' + SID)

    const other = 'sess-live-2'
    routes['/control/sessions/' + other] = () => live(other)
    r.rerender(<SessionDetailModal session={{ id: other, state: 'active' }} onClose={() => {}} />)
    await flush()
    expect(first.opts.signal.aborted).toBe(true)
    expect(r.container.querySelector('.detail-session-id').textContent).toContain(other)

    slow.resolve(json({ violations: [matchRow], semantic_decisions: [decisionA] }))
    await flush()
    expect(r.container.querySelector('.evidence')).toBeNull()
  })

  it('never lets a slow earlier poll overwrite a newer response', async () => {
    const slowDetail = deferred()
    const slowFlagged = deferred()
    let detailCalls = 0
    let flaggedCalls = 0
    routes['/control/sessions/' + SID] = () => (++detailCalls === 1 ? slowDetail.promise : live(SID, { correlation_shadow: [shadowMatch] }))
    routes['/control/flagged/' + SID] = () => (++flaggedCalls === 1 ? slowFlagged.promise : json({ violations: [matchRow], semantic_decisions: [decisionA] }))
    const r = open()
    await flush()
    await tick()
    expect(r.container.querySelectorAll('.evidence-decision').length).toBe(1)
    expect(r.container.querySelector('.timeline-dot-shadow_match')).toBeTruthy()

    // The first tick's responses arrive last and carry nothing.
    slowDetail.resolve(live(SID))
    slowFlagged.resolve(json({ violations: [], semantic_decisions: [] }))
    await flush()
    expect(r.container.querySelectorAll('.evidence-decision').length).toBe(1)
    expect(r.container.querySelector('.timeline-dot-shadow_match')).toBeTruthy()
  })

  const TURNS = '/control/sessions/' + SID + '/turns?full=true'

  it('refreshes the expensive endpoints only when a live counter changes', async () => {
    routes['/control/sessions/' + SID] = () => live(SID, { tool_calls: 1, risk_score: 5 })
    const r = open()
    await flush()
    expect(count('/control/flagged/' + SID)).toBe(1)
    expect(count(TURNS)).toBe(1)

    // Nothing changed on the next ticks: the cheap detail is polled, the
    // content-bearing endpoints are not.
    await tick()
    await tick()
    expect(count('/control/sessions/' + SID)).toBe(3)
    expect(count('/control/flagged/' + SID)).toBe(1)
    expect(count(TURNS)).toBe(1)

    // A counter moves and new evidence is present: both are refetched once.
    routes['/control/sessions/' + SID] = () => live(SID, { tool_calls: 2, risk_score: 5 })
    routes['/control/flagged/' + SID] = () => json({ violations: [matchRow], semantic_decisions: [decisionA] })
    await tick()
    expect(count('/control/flagged/' + SID)).toBe(2)
    expect(count(TURNS)).toBe(2)
    expect(r.container.querySelectorAll('.evidence-decision').length).toBe(1)

    // Same counters again: no further refetch.
    await tick()
    expect(count('/control/flagged/' + SID)).toBe(2)
    expect(count(TURNS)).toBe(2)

    // Each tracked counter triggers a refresh.
    routes['/control/sessions/' + SID] = () => live(SID, { tool_calls: 2, risk_score: 9 })
    await tick()
    expect(count('/control/flagged/' + SID)).toBe(3)
    routes['/control/sessions/' + SID] = () => live(SID, { tool_calls: 2, risk_score: 9, request_count: 4 })
    await tick()
    expect(count('/control/flagged/' + SID)).toBe(4)
    routes['/control/sessions/' + SID] = () => live(SID, { tool_calls: 2, risk_score: 9, request_count: 4, message_count: 6 })
    await tick()
    expect(count('/control/flagged/' + SID)).toBe(5)
    expect(count(TURNS)).toBe(5)
  })

  it('retries the evidence on the next tick when the refresh it was triggered for failed', async () => {
    routes['/control/sessions/' + SID] = () => live(SID, { message_count: 1 })
    const r = open()
    await flush()
    expect(count('/control/flagged/' + SID)).toBe(1)

    // The counter moves, but the flagged fetch errors: nothing is applied.
    routes['/control/sessions/' + SID] = () => live(SID, { message_count: 2 })
    routes['/control/flagged/' + SID] = () => json({}, 500)
    await tick()
    expect(count('/control/flagged/' + SID)).toBe(2)
    expect(r.container.querySelector('.evidence')).toBeNull()

    // Nothing else changes, yet the failed refresh is retried and renders.
    routes['/control/flagged/' + SID] = () => json({ violations: [matchRow], semantic_decisions: [decisionA] })
    await tick()
    expect(count('/control/flagged/' + SID)).toBe(3)
    expect(r.container.querySelectorAll('.evidence-decision').length).toBe(1)

    // Applied: no further refetch while the signal holds.
    await tick()
    expect(count('/control/flagged/' + SID)).toBe(3)
  })

  it('retries a rejected evidence fetch, and a persistently failing endpoint at most once per tick', async () => {
    routes['/control/sessions/' + SID] = () => live(SID, { message_count: 1 })
    open()
    await flush()
    routes['/control/sessions/' + SID] = () => live(SID, { message_count: 2 })
    routes['/control/flagged/' + SID] = () => Promise.reject(new Error('dropped'))
    await tick()
    await tick()
    await tick()
    expect(count('/control/flagged/' + SID)).toBe(4)
    // The healthy turns endpoint is fetched once for the change, not per retry.
    expect(count(TURNS)).toBe(2)
  })

  it('refreshes the evidence when only message_count moves', async () => {
    routes['/control/sessions/' + SID] = () => live(SID, { message_count: 1 })
    open()
    await flush()
    await tick()
    expect(count('/control/flagged/' + SID)).toBe(1)
    routes['/control/sessions/' + SID] = () => live(SID, { message_count: 2 })
    await tick()
    expect(count('/control/flagged/' + SID)).toBe(2)
  })

  it('shows no evidence section for a completed session with no evidence', async () => {
    routes['/control/sessions/' + SID] = () => json({ id: SID, state: 'completed' })
    routes['/control/history/' + SID] = () => json({}, 404)
    const r = open()
    await flush()
    expect(r.container.querySelector('.evidence')).toBeNull()
    expect(r.container.querySelector('.evidence-pending')).toBeNull()
    for (let i = 0; i < 8; i++) await tick()
    expect(r.container.querySelector('.evidence-unavailable')).toBeNull()
    expect(r.container.querySelector('.evidence')).toBeNull()
    expect(r.container.textContent).not.toContain('Semantic evidence')
  })
})
