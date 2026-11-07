import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, waitFor, cleanup, fireEvent } from '@testing-library/preact'
import { App } from './App'
import { AUTH_KEY } from './apiFetch'
import fixture from './components/__fixtures__/history-correlation.json'

describe('App auth gating', () => {
  let mockFetch
  let storage

  beforeEach(() => {
    storage = {}
    vi.stubGlobal('localStorage', {
      getItem: vi.fn((k) => storage[k] ?? null),
      setItem: vi.fn((k, v) => { storage[k] = String(v) }),
      removeItem: vi.fn((k) => { delete storage[k] }),
    })
    mockFetch = vi.fn()
    vi.stubGlobal('fetch', mockFetch)
  })

  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it('skips login when server has no auth configured (health 200)', async () => {
    mockFetch.mockResolvedValue({ status: 200, ok: true, json: () => Promise.resolve({}) })

    const { container } = render(<App />)

    await waitFor(() => {
      expect(container.querySelector('.topnav')).toBeTruthy()
    })
    expect(container.querySelector('.login-container')).toBeNull()
  })

  it('shows login when server requires auth (health 401)', async () => {
    mockFetch.mockResolvedValue({ status: 401, ok: false })

    const { container } = render(<App />)

    await waitFor(() => {
      expect(container.querySelector('.login-container')).toBeTruthy()
    })
    expect(container.querySelector('.topnav')).toBeNull()
  })

  it('skips probe and renders dashboard when key exists in localStorage', async () => {
    storage[AUTH_KEY] = 'existing-key'
    mockFetch.mockResolvedValue({ status: 200, ok: true, json: () => Promise.resolve({}) })

    const { container } = render(<App />)

    await waitFor(() => {
      expect(container.querySelector('.topnav')).toBeTruthy()
    })
    // Should not have probed health without credentials
    const healthCalls = mockFetch.mock.calls.filter(
      ([url]) => url === '/control/health' && !mockFetch.mock.calls[0]?.[1]?.headers?.Authorization
    )
    // The first fetch should be an authed call (stats etc.), not an unauthed health probe
    expect(container.querySelector('.login-container')).toBeNull()
  })
})

// History page session details: the persisted record's decisions, matches
// and shadow lists render through SemanticEvidence, and every link a match
// emits resolves to exactly one rendered row.
describe('History session details semantic evidence', () => {
  let routes

  function json(body, status = 200) {
    return { status, ok: status < 400, json: () => Promise.resolve(body) }
  }

  beforeEach(() => {
    const storage = { [AUTH_KEY]: 'k' }
    vi.stubGlobal('localStorage', {
      getItem: vi.fn((k) => storage[k] ?? null),
      setItem: vi.fn((k, v) => { storage[k] = String(v) }),
      removeItem: vi.fn((k) => { delete storage[k] }),
    })
    routes = {}
    vi.stubGlobal('fetch', vi.fn((url) => Promise.resolve(routes[url] ? routes[url]() : json({}))))
  })

  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  async function openHistoryRow(record) {
    routes['/control/history?limit=50&offset=0'] = () => json({ sessions: [record], total_count: 1 })
    routes['/control/flagged/' + record.id] = () => json({}, 404)
    routes['/control/history/' + record.id] = () => json(record)
    const r = render(<App />)
    await waitFor(() => expect(r.container.querySelector('.topnav')).toBeTruthy())
    const nav = [...r.container.querySelectorAll('.topnav-item')].find((b) => b.textContent.includes('History'))
    fireEvent.click(nav)
    await waitFor(() => expect(r.container.querySelector('tr.clickable')).toBeTruthy())
    fireEvent.click(r.container.querySelector('tr.clickable'))
    await waitFor(() => expect(r.container.querySelector('.evidence-match')).toBeTruthy())
    return r
  }

  it('renders the persisted record with every match link resolving to one rendered row', async () => {
    const r = await openHistoryRow(fixture)
    await waitFor(() => expect(r.container.querySelectorAll('.evidence-decision').length).toBe(3))
    expect(r.container.querySelectorAll('.evidence-decision').length).toBe(3)
    expect(r.container.querySelectorAll('.violation-item').length).toBe(2)
    const links = r.container.querySelectorAll('a.evidence-link')
    expect(links.length).toBe(3)
    for (const a of links) {
      const id = a.getAttribute('href').slice(1)
      expect(r.container.querySelectorAll('[id="' + id + '"]').length).toBe(1)
    }
    const target = r.container.querySelector(links[0].getAttribute('href'))
    expect(target.className).toContain('evidence-decision')
    fireEvent.click(links[0])
    // The semantic violation's row does not duplicate its decision row's ID.
    expect(r.container.querySelector('.violation-item[id]')).toBeNull()
    // Shadow data stays collapsed but present.
    fireEvent.click(r.container.querySelector('.evidence-shadow-toggle'))
    expect(r.container.querySelector('.evidence-shadow-body').textContent).toContain('cm_fedcba9876543210fedcba9876543210')
  })

  it('anchors a non-semantic violation row so a match can link to it', async () => {
    const regex = { rule_name: 'prompt_injection_probe', event_id: 'ev_regex', event_category: 'prompt_injection', severity: 'warning', timestamp: '2026-10-20T09:01:00Z' }
    const match = { rule_name: 'correlation.probe_twice', event_id: 'cm_1', event_category: 'correlation_match', severity: 'warning', evidence_only: true, timestamp: '2026-10-20T09:02:00Z',
      correlation: { match_id: 'cm_1', rule: 'probe_twice', shape: 'repeated', mode: 'audit', event_ids: ['ev_regex'], categories: ['prompt_injection'], first_event: '2026-10-20T09:01:00Z', last_event: '2026-10-20T09:02:00Z' } }
    const r = await openHistoryRow({ ...fixture, id: 'sess-regex', violations: [regex, match], semantic_decisions: [], semantic_shadow: [], correlation_shadow: [] })
    const link = r.container.querySelector('a.evidence-link')
    expect(link.getAttribute('href')).toBe('#evidence-ev_regex')
    const targets = r.container.querySelectorAll('[id="evidence-ev_regex"]')
    expect(targets.length).toBe(1)
    expect(targets[0].className).toContain('violation-item')
  })

  it('shows persisted correlation evidence from the list row when both detail fetches fail', async () => {
    const regex = { rule_name: 'prompt_injection_probe', event_id: 'ev_regex', event_category: 'prompt_injection', severity: 'warning', timestamp: '2026-10-20T09:01:00Z' }
    const match = { rule_name: 'correlation.probe_twice', event_id: 'cm_1', event_category: 'correlation_match', severity: 'warning', evidence_only: true, timestamp: '2026-10-20T09:02:00Z',
      correlation: { match_id: 'cm_1', rule: 'probe_twice', shape: 'repeated', mode: 'audit', event_ids: ['ev_regex'], categories: ['prompt_injection'], first_event: '2026-10-20T09:01:00Z', last_event: '2026-10-20T09:02:00Z' } }
    const row = { ...fixture, id: 'sess-rowonly', violations: [regex, match] }
    routes['/control/history?limit=50&offset=0'] = () => json({ sessions: [row], total_count: 1 })
    routes['/control/flagged/sess-rowonly'] = () => json({}, 500)
    routes['/control/history/sess-rowonly'] = () => json({}, 500)
    const r = render(<App />)
    await waitFor(() => expect(r.container.querySelector('.topnav')).toBeTruthy())
    fireEvent.click([...r.container.querySelectorAll('.topnav-item')].find((b) => b.textContent.includes('History')))
    await waitFor(() => expect(r.container.querySelector('tr.clickable')).toBeTruthy())
    fireEvent.click(r.container.querySelector('tr.clickable'))
    await waitFor(() => expect(r.container.querySelector('.evidence-match')).toBeTruthy())
  })
})
