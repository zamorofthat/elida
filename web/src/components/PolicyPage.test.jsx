import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, waitFor, cleanup } from '@testing-library/preact'
import { PolicyPage, correlationSummary } from './PolicyPage'

const config = { mode: 'enforce', rules: [], risk_ladder: { enabled: true } }
const statsWithCorrelation = {
  total_flagged: 1, critical: 0, warning: 1, info: 0, rules_count: 0, risk_ladder: true, high_risk: 0, throttled: 0, blocked: 0, avg_risk_score: 0,
  correlation: { rules: 1, shards: 4, sessions: 2, history_events: 5, events_observed: 7, events_ignored: 0, duplicate_events: 0, events_dropped: 0,
    history_evicted: 0, sessions_evicted: 0, matches: 1, cooldown_suppressed: 0, duplicate_matches: 0,
    events_queued: 7, matches_recorded: 1, matches_duplicate: 0, matches_discarded: 0,
    tool_batches_queued: 3, tool_batches_dropped: 1, tool_calls_dropped: 0, tool_batches_discarded: 0, tool_sessions_evicted: 0, tool_batches_refused: 0, tool_batches_abandoned: 0 },
}

function json(body) {
  return { status: 200, ok: true, json: () => Promise.resolve(body), text: () => Promise.resolve(JSON.stringify(body)) }
}

describe('PolicyPage correlation card', () => {
  let stats
  beforeEach(() => {
    stats = statsWithCorrelation
    vi.stubGlobal('localStorage', { getItem: vi.fn(() => 'k'), setItem: vi.fn(), removeItem: vi.fn() })
    vi.stubGlobal('fetch', vi.fn((url) => {
      if (url.endsWith('/control/policy')) return Promise.resolve(json(config))
      if (url.endsWith('/control/flagged/stats')) return Promise.resolve(json(stats))
      if (url.endsWith('/control/flagged')) return Promise.resolve(json({ flagged: [], count: 0 }))
      return Promise.resolve(json({}))
    }))
  })
  afterEach(() => { cleanup(); vi.unstubAllGlobals() })

  it('summarizes the policy correlation counters', () => {
    expect(correlationSummary(statsWithCorrelation)).toEqual({ sessions: 2, matches_recorded: 1, events_queued: 7, events_observed: 7, sessions_evicted: 0, history_evicted: 0, tool_batches_dropped: 1 })
    expect(correlationSummary({ total_flagged: 0 })).toBeNull()
    expect(correlationSummary(null)).toBeNull()
  })

  it('renders the card when the stats carry correlation', async () => {
    const { container } = render(<PolicyPage />)
    await waitFor(() => expect(container.querySelector('.policy-correlation-card')).toBeTruthy())
    const text = container.querySelector('.policy-correlation-card').textContent
    expect(text).toContain('Correlation')
    expect(text).toContain('1')
    expect(text).toContain('2 sessions')
    expect(text).toContain('1 tool batch dropped')
  })

  it('renders no card when correlation is off', async () => {
    stats = { total_flagged: 0 }
    const { container } = render(<PolicyPage />)
    await waitFor(() => expect(container.querySelector('.policy-preset-row')).toBeTruthy())
    expect(container.querySelector('.policy-correlation-card')).toBeNull()
  })
})
