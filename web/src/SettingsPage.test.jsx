import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, waitFor, cleanup, fireEvent } from '@testing-library/preact'
import { App } from './App'
import { AUTH_KEY } from './apiFetch'

// SettingsPage wiring for the decision section: the enforce confirmation,
// the server's refusal shown verbatim, and the status refresh after a save.

const settings = {
  policy: { enabled: true, mode: 'enforce', preset: 'standard', risk_ladder: {}, custom_rules: [] },
  capture: {}, failover: {},
  decision: { mode: 'audit', preprocessing: { max_input_bytes: 262144, max_analysis_bytes: 524288,
    max_representations: 8, max_decode_depth: 2, max_expansion_ratio: 4 } },
}
const decisionStatus = {
  enabled: true, mode: 'audit', effective_mode: 'audit', capability: 'inline',
  threshold_set: 'v1', threshold_set_matches: true, coverage_gaps: {},
  limits: { threshold_set: 'v1', elevated_threshold: 0.3, inline_timeout_ms: 50, max_concurrency: 4,
    max_inline_tokens: 128, max_inline_windows: 1, max_async_windows: 8, async_queue_size: 100 },
}

function json(body, status = 200) {
  return { status, ok: status < 400, json: () => Promise.resolve(body), text: () => Promise.resolve(JSON.stringify(body)) }
}

describe('SettingsPage decision section', () => {
  let mockFetch
  let putResponse
  let currentStatus
  let diffBody

  beforeEach(() => {
    const storage = { [AUTH_KEY]: 'k' }
    vi.stubGlobal('localStorage', {
      getItem: vi.fn((k) => storage[k] ?? null),
      setItem: vi.fn((k, v) => { storage[k] = String(v) }),
      removeItem: vi.fn((k) => { delete storage[k] }),
    })
    putResponse = json({ status: 'saved' })
    currentStatus = decisionStatus
    diffBody = { modified_count: 0, modifications: {} }
    mockFetch = vi.fn((url, opts = {}) => {
      if (opts.method === 'PUT') return Promise.resolve(putResponse)
      if (url === '/control/settings') return Promise.resolve(json(settings))
      if (url === '/control/settings/diff') return Promise.resolve(json(diffBody))
      if (url === '/control/decision') return Promise.resolve(json(currentStatus))
      return Promise.resolve(json({}))
    })
    vi.stubGlobal('fetch', mockFetch)
  })

  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  async function openSettings() {
    const r = render(<App />)
    await waitFor(() => expect(r.container.querySelector('.topnav')).toBeTruthy())
    const nav = [...r.container.querySelectorAll('.topnav-item')].find((b) => b.textContent.includes('Settings'))
    fireEvent.click(nav)
    await waitFor(() => expect(r.container.querySelector('select[name="decision_mode"]')).toBeTruthy())
    // A status-only value: the fetched status reached the component.
    await waitFor(() => expect(r.container.querySelector('.decision-status')?.textContent).toContain('audit (effective: audit)'))
    return r
  }

  const puts = () => mockFetch.mock.calls.filter(([, o]) => o && o.method === 'PUT')
  const saveButton = (c) => [...c.querySelectorAll('button')].find((b) => b.textContent.includes('Save Settings'))

  it('cancelling the enforce confirmation sends nothing', async () => {
    vi.stubGlobal('confirm', vi.fn(() => false))
    const { container } = await openSettings()
    fireEvent.change(container.querySelector('select[name="decision_mode"]'), { target: { value: 'enforce' } })
    fireEvent.click(saveButton(container))
    await waitFor(() => expect(confirm).toHaveBeenCalled())
    expect(puts()).toHaveLength(0)
  })

  it('a confirmed change to enforce sends ?confirm=enforce and refreshes the status', async () => {
    vi.stubGlobal('confirm', vi.fn(() => true))
    const { container } = await openSettings()
    const before = mockFetch.mock.calls.filter(([u]) => u === '/control/decision').length
    currentStatus = { ...decisionStatus, mode: 'enforce', effective_mode: 'audit',
      reason: 'enforce capped to audit because policy.mode is audit' }
    fireEvent.change(container.querySelector('select[name="decision_mode"]'), { target: { value: 'enforce' } })
    fireEvent.click(saveButton(container))
    await waitFor(() => expect(puts()).toHaveLength(1))
    expect(puts()[0][0]).toBe('/control/settings?confirm=enforce')
    expect(JSON.parse(puts()[0][1].body).decision.mode).toBe('enforce')
    await waitFor(() => expect(mockFetch.mock.calls.filter(([u]) => u === '/control/decision').length).toBe(before + 1))
    // The refreshed status is what renders.
    await waitFor(() => expect(container.querySelector('.decision-status').textContent).toContain('enforce (effective: audit)'))
    expect(container.querySelector('.decision-status').textContent).toContain('enforce capped to audit because policy.mode is audit')
  })

  it('a non-enforce edit saves without confirmation', async () => {
    vi.stubGlobal('confirm', vi.fn(() => true))
    const { container } = await openSettings()
    fireEvent.change(container.querySelector('select[name="decision_mode"]'), { target: { value: 'shadow' } })
    fireEvent.click(saveButton(container))
    await waitFor(() => expect(puts()).toHaveLength(1))
    expect(puts()[0][0]).toBe('/control/settings')
    expect(confirm).not.toHaveBeenCalled()
  })

  it('a refused save shows the server problems verbatim and keeps the edit', async () => {
    putResponse = json({ error: 'settings refused', problems: [
      'decision.max_decode_depth: 99 exceeds the ceiling of 8'] }, 400)
    const { container } = await openSettings()
    const settingsGets = () => mockFetch.mock.calls.filter(([u, o]) => u === '/control/settings' && !(o && o.method)).length
    const getsBefore = settingsGets()
    fireEvent.change(container.querySelector('select[name="decision_mode"]'), { target: { value: 'shadow' } })
    fireEvent.click(saveButton(container))
    await waitFor(() => expect(container.querySelector('.settings-message.error')).toBeTruthy())
    expect(container.querySelector('.settings-message.error').textContent)
      .toBe('Failed to save: decision.max_decode_depth: 99 exceeds the ceiling of 8')
    expect(container.querySelector('select[name="decision_mode"]').value).toBe('shadow')
    expect(settingsGets()).toBe(getsBefore)
  })

  it('a modified decision key lights its badge and the header counts the modifications', async () => {
    diffBody = { modified_count: 1, modifications: {
      'decision.mode': { path: 'decision.mode', default_value: 'shadow', local_value: 'audit' } } }
    const { container } = await openSettings()
    await waitFor(() => expect(container.querySelector('.settings-diff-count')?.textContent).toBe('1 setting modified'))
    const item = container.querySelector('#decision-mode').closest('.settings-item')
    expect(item.classList.contains('modified')).toBe(true)
    expect(item.querySelector('.modified-badge')).toBeTruthy()
    expect(container.querySelectorAll('.modified-badge')).toHaveLength(1)
  })

  it('an emptied budget is flagged and blocks the save with a client message', async () => {
    const { container } = await openSettings()
    fireEvent.input(container.querySelector('[name="max_decode_depth"]'), { target: { value: '' } })
    await waitFor(() => expect(container.querySelector('#decision-max_decode_depth-error')).toBeTruthy())
    fireEvent.click(saveButton(container))
    await waitFor(() => expect(container.querySelector('.settings-message.error')).toBeTruthy())
    expect(container.querySelector('.settings-message.error').textContent)
      .toBe('Not saved: enter a whole number of 0 or more for decision.preprocessing.max_decode_depth')
    expect(puts()).toHaveLength(0)
  })
})
