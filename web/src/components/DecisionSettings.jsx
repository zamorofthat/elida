// Settings page section for semantic injection detection (decision.*).
//
// The server stays authoritative: only decision.mode and
// decision.preprocessing.* are editable at runtime, and PUT /control/settings
// refuses anything else with a 400 naming the key. The UI mirrors those
// guardrails (enforce greyed out, restart-only values display-only) but never
// replaces them: a refusal is shown exactly as the server worded it.

import { Fragment } from 'preact'

export const RUNTIME_PREPROCESSING_KEYS = ['max_input_bytes', 'max_analysis_bytes', 'max_representations', 'max_decode_depth', 'max_expansion_ratio']
const RESTART_ONLY = ['threshold_set', 'elevated_threshold', 'inline_timeout_ms', 'max_concurrency', 'max_inline_tokens', 'max_inline_windows', 'max_async_windows', 'async_queue_size']
const MODES = ['disabled', 'shadow', 'audit', 'enforce']

export function enforceBlockedReason(policyMode, status) {
  if (policyMode === 'audit') return 'enforce is unavailable while policy.mode is audit'
  if (!status || !status.threshold_set_matches) return 'enforce needs a threshold set that matches the loaded model'
  return null
}

// settingsSaveUrl adds ?confirm=enforce only on a change to enforce; the
// server refuses that change without it.
export function settingsSaveUrl(base, savedMode, nextMode) {
  const url = base + '/control/settings'
  return nextMode === 'enforce' && savedMode !== 'enforce' ? url + '?confirm=enforce' : url
}

// saveErrorText turns a refused save's response body into the message shown:
// the server's problems list verbatim, else its error, else the plain text.
export function saveErrorText(text) {
  try {
    const body = JSON.parse(text)
    if (body && Array.isArray(body.problems) && body.problems.length > 0) return body.problems.join('; ')
    if (body && typeof body.error === 'string') return body.error
  } catch (_) {
    // plain-text error (http.Error)
  }
  return String(text || '').trim()
}

function label(key) {
  return key.replaceAll('_', ' ')
}

export function DecisionSettings({ decision, policyMode, status, isModified, onChange, onPreprocessingChange }) {
  const running = !!(status && status.enabled && status.capability !== 'disabled')
  const blocked = enforceBlockedReason(policyMode, status)
  const pre = (decision && decision.preprocessing) || {}
  const gaps = status && status.coverage_gaps ? Object.values(status.coverage_gaps).reduce((a, b) => a + b, 0) : 0
  const modeModified = isModified('decision.mode')
  return (
    <div class="panel">
      <div class="panel-header">
        <h2 class="panel-title">Semantic Detection</h2>
      </div>
      <div class="panel-body">
        {!running && (
          <p class="settings-note">
            Semantic detection is not running. Changes here take effect only after it is enabled in elida.yaml and ELIDA restarts.
          </p>
        )}
        {status && (
          <dl class="decision-status">
            <dt>Capability</dt>
            <dd>{status.capability}</dd>
            <dt>Mode</dt>
            <dd>{status.mode || '-'} (effective: {status.effective_mode || '-'})</dd>
            {status.reason && (
              <>
                <dt>Reason</dt>
                <dd>{status.reason}</dd>
              </>
            )}
            <dt>Model</dt>
            <dd>
              {[status.model, status.model_version, status.model_checksum ? status.model_checksum.slice(0, 12) : '']
                .filter(Boolean).join(' ') || '-'}
            </dd>
            <dt>Threshold set</dt>
            <dd>{status.threshold_set || '-'} {status.threshold_set_matches ? '(matches model)' : '(does not match model)'}</dd>
            <dt>Inline completion</dt>
            <dd>{Math.round((status.inline_completion_ratio || 0) * 100)}%</dd>
            <dt>Async queue</dt>
            <dd>{status.async_queue_depth || 0} queued, {status.async_dropped || 0} dropped</dd>
            <dt>Coverage gaps</dt>
            <dd>{gaps}</dd>
          </dl>
        )}
        <div class="settings-grid">
          <div class={'settings-item' + (modeModified ? ' modified' : '')}>
            <label class="settings-label" for="decision-mode">
              <span>Detection Mode</span>
              {modeModified && <span class="modified-badge">modified</span>}
            </label>
            <select
              id="decision-mode"
              class="settings-select"
              name="decision_mode"
              value={decision?.mode || 'shadow'}
              onChange={(e) => onChange('mode', e.target.value)}
            >
              {MODES.map((m) => (
                <option key={m} value={m} disabled={m === 'enforce' && !!blocked}>{m}</option>
              ))}
            </select>
            {blocked && <p class="settings-note">{blocked}</p>}
          </div>
          {RUNTIME_PREPROCESSING_KEYS.map((k) => {
            const modified = isModified('decision.preprocessing.' + k)
            return (
              <div key={k} class={'settings-item' + (modified ? ' modified' : '')}>
                <label class="settings-label" for={'decision-' + k}>
                  <span>{label(k)}</span>
                  {modified && <span class="modified-badge">modified</span>}
                </label>
                <input
                  id={'decision-' + k}
                  class="settings-input"
                  type="number"
                  min="0"
                  name={k}
                  value={pre[k] ?? ''}
                  onInput={(e) => onPreprocessingChange(k, parseInt(e.target.value, 10))}
                />
              </div>
            )
          })}
        </div>
        {status && status.limits && (
          <>
            <p class="settings-note">Restart-only: set these in elida.yaml; a restart is required to change them.</p>
            <dl class="decision-limits">
              {RESTART_ONLY.map((k) => (
                <Fragment key={k}>
                  <dt>{label(k)}</dt>
                  <dd>{String(status.limits[k] ?? '-')}</dd>
                </Fragment>
              ))}
            </dl>
          </>
        )}
      </div>
    </div>
  )
}
