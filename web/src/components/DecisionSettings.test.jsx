import { describe, it, expect, vi, afterEach } from 'vitest'
import { render, cleanup, fireEvent } from '@testing-library/preact'
import { DecisionSettings, enforceBlockedReason, settingsSaveUrl, saveErrorText, isValidBudget, invalidBudgetKeys } from './DecisionSettings'

const status = {
  enabled: true, mode: 'audit', effective_mode: 'audit', capability: 'inline',
  model: 'minilm-multihead', model_version: 'v5-fp32', model_checksum: 'abcdef0123456789',
  threshold_set: 'v1', threshold_set_matches: true, inline_completion_ratio: 0.97,
  async_queue_depth: 2, async_dropped: 0, coverage_gaps: { not_assessed: 3 },
  limits: { threshold_set: 'v1', elevated_threshold: 0.3, inline_timeout_ms: 50, max_concurrency: 4,
    max_inline_tokens: 128, max_inline_windows: 1, max_async_windows: 8, async_queue_size: 100 },
}
const decision = { mode: 'audit', preprocessing: { max_input_bytes: 262144, max_analysis_bytes: 524288,
  max_representations: 8, max_decode_depth: 2, max_expansion_ratio: 4 } }

function renderIt(over = {}) {
  const props = { decision, policyMode: 'enforce', status, isModified: () => false,
    onChange: vi.fn(), onPreprocessingChange: vi.fn(), ...over }
  return { props, ...render(<DecisionSettings {...props} />) }
}

describe('DecisionSettings', () => {
  afterEach(() => cleanup())

  it('shows the read-only status block', () => {
    const { container } = renderIt()
    const text = container.textContent
    expect(text).toContain('inline')
    expect(text).toContain('minilm-multihead')
    expect(text).toContain('v1')
  })

  it('shows the effective mode and the reason it differs', () => {
    const { container } = renderIt({ status: { ...status, mode: 'enforce', effective_mode: 'audit',
      reason: 'enforce capped to audit because policy.mode is audit' } })
    expect(container.textContent).toContain('enforce capped to audit because policy.mode is audit')
  })

  it('renders restart-only values as text, not inputs', () => {
    const { container } = renderIt()
    expect(container.textContent).toContain('restart')
    expect(container.textContent).toContain('elida.yaml')
    expect(container.textContent).toContain('100')
    expect(container.querySelector('[name="max_concurrency"]')).toBeNull()
    expect(container.querySelector('[name="threshold_set"]')).toBeNull()
    expect(container.querySelector('[name="max_decode_depth"]')).toBeTruthy()
  })

  it('labels every runtime input', () => {
    const { container } = renderIt()
    const fields = container.querySelectorAll('select, input')
    expect(fields.length).toBe(6)
    for (const el of fields) {
      expect(el.id).toBeTruthy()
      expect(container.querySelector('label[for="' + el.id + '"]')).toBeTruthy()
    }
  })

  it('disables enforce under policy.mode audit and says why', () => {
    const { container } = renderIt({ policyMode: 'audit' })
    expect(container.querySelector('option[value="enforce"]').disabled).toBe(true)
    expect(container.textContent).toContain('policy.mode is audit')
  })

  it('disables enforce when the threshold set does not match', () => {
    const { container } = renderIt({ status: { ...status, threshold_set_matches: false } })
    expect(container.querySelector('option[value="enforce"]').disabled).toBe(true)
  })

  it('leaves enforce selectable when nothing blocks it', () => {
    const { container } = renderIt()
    expect(container.querySelector('option[value="enforce"]').disabled).toBe(false)
  })

  it('reports mode and budget edits', () => {
    const { container, props } = renderIt()
    fireEvent.change(container.querySelector('select[name="decision_mode"]'), { target: { value: 'shadow' } })
    expect(props.onChange).toHaveBeenCalledWith('mode', 'shadow')
    fireEvent.input(container.querySelector('[name="max_decode_depth"]'), { target: { value: '1' } })
    expect(props.onPreprocessingChange).toHaveBeenCalledWith('max_decode_depth', 1)
  })

  it('explains when semantic detection is not running', () => {
    const { container } = renderIt({ status: { ...status, enabled: false, capability: 'disabled' } })
    expect(container.textContent).toContain('not running')
    expect(container.textContent).toContain('decision changes are refused until it is enabled')
  })

  it('renders without a status (status fetch failed)', () => {
    const { container } = renderIt({ status: null })
    expect(container.querySelector('select[name="decision_mode"]')).toBeTruthy()
    expect(container.textContent).toContain('status unavailable')
    expect(container.textContent).not.toContain('not running')
  })

  it('shows the breaker state', () => {
    const closed = renderIt()
    expect(closed.container.querySelector('.decision-status').textContent).toContain('Breakerclosed')
    cleanup()
    const open = renderIt({ status: { ...status, breaker_open: true } })
    expect(open.container.querySelector('.decision-status').textContent).toContain('Breakeropen')
  })

  it('shows a modified badge for a modified key', () => {
    const { container } = renderIt({ isModified: (p) => p === 'decision.preprocessing.max_decode_depth' })
    const item = container.querySelector('#decision-max_decode_depth').closest('.settings-item')
    expect(item.querySelector('.modified-badge')).toBeTruthy()
    expect(container.querySelectorAll('.modified-badge')).toHaveLength(1)
  })

  it('keeps an emptied or non-integer budget as text and flags it', () => {
    const { container, props } = renderIt()
    fireEvent.input(container.querySelector('[name="max_decode_depth"]'), { target: { value: '' } })
    expect(props.onPreprocessingChange).toHaveBeenLastCalledWith('max_decode_depth', '')
    cleanup()
    const flagged = renderIt({ decision: { ...decision, preprocessing: { ...decision.preprocessing, max_decode_depth: '' } } })
    const input = flagged.container.querySelector('[name="max_decode_depth"]')
    expect(input.getAttribute('aria-invalid')).toBe('true')
    expect(flagged.container.querySelector('#decision-max_decode_depth-error').textContent)
      .toBe('Enter a whole number of 0 or more.')
    expect(flagged.container.querySelector('[name="max_input_bytes"]').getAttribute('aria-invalid')).toBeNull()
  })
})

describe('guardrail helpers', () => {
  it('invalidBudgetKeys flags empty, fractional and negative budgets only', () => {
    expect(isValidBudget(0)).toBe(true)
    expect(isValidBudget(3)).toBe(true)
    for (const v of ['', '1.5', 1.5, -1, NaN, null]) expect(isValidBudget(v)).toBe(false)
    const pre = { ...decision.preprocessing, max_decode_depth: '', max_expansion_ratio: 1.5 }
    expect(invalidBudgetKeys({ preprocessing: pre })).toEqual(['max_decode_depth', 'max_expansion_ratio'])
    expect(invalidBudgetKeys(decision)).toEqual([])
    expect(invalidBudgetKeys(undefined)).toEqual([])
  })

  it('enforceBlockedReason', () => {
    expect(enforceBlockedReason('enforce', status)).toBeNull()
    expect(enforceBlockedReason('audit', status)).toContain('policy.mode is audit')
    expect(enforceBlockedReason('enforce', { ...status, threshold_set_matches: false })).toContain('threshold set')
  })
  it('settingsSaveUrl asks for confirmation only on a change to enforce', () => {
    expect(settingsSaveUrl('', 'audit', 'enforce')).toBe('/control/settings?confirm=enforce')
    expect(settingsSaveUrl('', 'enforce', 'enforce')).toBe('/control/settings')
    expect(settingsSaveUrl('', 'audit', 'shadow')).toBe('/control/settings')
    expect(settingsSaveUrl('http://x', null, 'enforce')).toBe('http://x/control/settings?confirm=enforce')
  })
  it('saveErrorText shows the server problems verbatim', () => {
    const body = JSON.stringify({ error: 'settings refused', problems: [
      'decision.max_concurrency: restart-only; set it in elida.yaml and restart',
      'decision.mode: enforce needs policy.mode enforce'] })
    expect(saveErrorText(body)).toBe('decision.max_concurrency: restart-only; set it in elida.yaml and restart; decision.mode: enforce needs policy.mode enforce')
    expect(saveErrorText(JSON.stringify({ error: 'settings refused' }))).toBe('settings refused')
    expect(saveErrorText('Failed to save settings\n')).toBe('Failed to save settings')
  })

  it('shows shadow_matches_dropped and the shadow correlator counters when present', () => {
    const { container } = renderIt({ status: { ...status, shadow_matches_dropped: 2,
      shadow_correlation: { rules: 1, shards: 4, sessions: 3, history_events: 9, events_observed: 12, events_ignored: 0,
        duplicate_events: 1, events_dropped: 0, history_evicted: 0, sessions_evicted: 1, matches: 4, cooldown_suppressed: 2, duplicate_matches: 0 } } })
    const text = container.querySelector('.decision-status').textContent
    expect(text).toContain('Shadow correlation')
    expect(text).toContain('4 matches')
    expect(text).toContain('3 sessions')
    expect(text).toContain('12 events')
    expect(text).toContain('2 suppressed')
    expect(text).toContain('1 evicted')
    expect(text).toContain('2 dropped')
  })

  it('shows only the dropped count when no shadow correlator runs', () => {
    const { container } = renderIt({ status: { ...status, shadow_matches_dropped: 0 } })
    const text = container.querySelector('.decision-status').textContent
    expect(text).toContain('Shadow correlation')
    expect(text).toContain('not running')
    expect(text).toContain('0 dropped')
  })
})
