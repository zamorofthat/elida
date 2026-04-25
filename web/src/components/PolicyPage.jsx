import { useState, useEffect, useRef } from 'preact/hooks'
import { apiFetch } from '../apiFetch'
import { truncateId } from '../utils'
import { SeverityBadge } from './shared/Badge'

// ---------------------------------------------------------------------------
// Policy Page
// ---------------------------------------------------------------------------

export function PolicyPage() {
  const [config, setConfig] = useState(null)
  const [flaggedStats, setFlaggedStats] = useState({})
  const [flagged, setFlagged] = useState([])
  const timerRef = useRef(null)

  const fetchAll = async () => {
    try {
      const [cfgRes, statsRes, flaggedRes] = await Promise.all([
        apiFetch('/control/policy'),
        apiFetch('/control/flagged/stats'),
        apiFetch('/control/flagged'),
      ])

      if (cfgRes.ok) setConfig(await cfgRes.json())
      if (statsRes.ok) setFlaggedStats(await statsRes.json())
      if (flaggedRes.ok) {
        const data = await flaggedRes.json()
        setFlagged(data.flagged || [])
      }
    } catch (err) {
      console.error('Policy fetch error:', err)
    }
  }

  useEffect(() => {
    fetchAll()
    timerRef.current = setInterval(() => {
      if (!document.hidden) fetchAll()
    }, 10000)
    return () => clearInterval(timerRef.current)
  }, [])

  // Collect all violations across flagged sessions, sorted by time desc
  const allViolations = []
  flagged.forEach(f => {
    (f.violations || []).forEach(v => {
      allViolations.push({ ...v, session_id: f.session_id })
    })
  })
  allViolations.sort((a, b) => new Date(b.timestamp) - new Date(a.timestamp))
  const recentViolations = allViolations.slice(0, 50)

  // Count violations per rule for the rules table
  const violationCounts = {}
  allViolations.forEach(v => {
    violationCounts[v.rule_name] = (violationCounts[v.rule_name] || 0) + 1
  })

  const rules = config?.rules || []
  const mode = config?.mode || 'unknown'
  const riskLadder = config?.risk_ladder || {}

  return (
    <div class="policy-page">

      {/* Preset card */}
      <div class="policy-preset-row">
        <div class="policy-preset-card">
          <div class="policy-preset-label">Policy Mode</div>
          <div class={'policy-preset-value' + (mode === 'enforce' ? ' enforce' : ' audit')}>
            {mode}
          </div>
        </div>
        <div class="policy-preset-card">
          <div class="policy-preset-label">Rules Loaded</div>
          <div class="policy-preset-value">{rules.length}</div>
        </div>
        <div class="policy-preset-card">
          <div class="policy-preset-label">Risk Ladder</div>
          <div class="policy-preset-value">
            {riskLadder.enabled ? 'Enabled' : 'Disabled'}
          </div>
        </div>
        <div class="policy-preset-card">
          <div class="policy-preset-label">Flagged Sessions</div>
          <div class="policy-preset-value">
            {flaggedStats.total_flagged || 0}
            {(flaggedStats.critical || 0) > 0 && (
              <span class="policy-critical-count"> ({flaggedStats.critical} critical)</span>
            )}
          </div>
        </div>
      </div>

      {/* Rules table */}
      <div class="policy-panel">
        <div class="policy-panel-header">
          <span class="policy-panel-title">Policy Rules</span>
          <span class="policy-panel-count">{rules.length} rules</span>
        </div>
        {rules.length === 0 ? (
          <div class="policy-empty">No rules configured</div>
        ) : (
          <div class="policy-rules-table-wrap">
            <table class="policy-rules-table">
              <thead>
                <tr>
                  <th>Rule</th>
                  <th>Type</th>
                  <th>Target</th>
                  <th>Severity</th>
                  <th>Action</th>
                  <th>Matches</th>
                </tr>
              </thead>
              <tbody>
                {rules.map((rule, i) => {
                  const matches = violationCounts[rule.name] || 0
                  return (
                    <tr key={i} class={matches > 0 ? 'policy-rule-matched' : ''}>
                      <td>
                        <div class="policy-rule-name">{rule.name}</div>
                        {rule.description && (
                          <div class="policy-rule-desc">{rule.description}</div>
                        )}
                      </td>
                      <td class="mono">{rule.type}</td>
                      <td class="mono">{rule.target || 'both'}</td>
                      <td><SeverityBadge severity={rule.severity} /></td>
                      <td>
                        <span class={'policy-action-badge action-' + (rule.action || 'flag')}>
                          {rule.action || 'flag'}
                        </span>
                      </td>
                      <td>
                        {matches > 0 ? (
                          <span class="policy-match-count">{matches}</span>
                        ) : (
                          <span class="policy-no-matches">\u2014</span>
                        )}
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        )}
      </div>

      {/* Recent violations */}
      <div class="policy-panel">
        <div class="policy-panel-header">
          <span class="policy-panel-title">Recent Violations</span>
          <span class="policy-panel-count">{allViolations.length} total</span>
        </div>
        {recentViolations.length === 0 ? (
          <div class="policy-empty">No violations recorded</div>
        ) : (
          <div class="policy-violations-list">
            {recentViolations.map((v, i) => (
              <div key={i} class="policy-violation-row">
                <span class="policy-violation-time">
                  {v.timestamp ? new Date(v.timestamp).toLocaleTimeString() : ''}
                </span>
                <span class="mono policy-violation-session">{truncateId(v.session_id)}</span>
                <SeverityBadge severity={v.severity} />
                <span class="policy-violation-rule">{v.rule_name}</span>
                {v.matched_text && (
                  <code class="policy-violation-snippet">{v.matched_text.slice(0, 60)}</code>
                )}
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}
