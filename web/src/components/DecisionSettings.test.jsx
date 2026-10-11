import { describe, it, expect, vi, afterEach } from 'vitest'
import { render, cleanup, fireEvent } from '@testing-library/preact'
import { DecisionSettings, enforceBlockedReason, settingsSaveUrl, saveErrorText } from './DecisionSettings'

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
  })

  it('renders without a status (status fetch failed)', () => {
    const { container } = renderIt({ status: null })
    expect(container.querySelector('select[name="decision_mode"]')).toBeTruthy()
  })
})

describe('guardrail helpers', () => {
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
})
