import { useState, useEffect } from 'preact/hooks'
import { apiFetch } from '../apiFetch'

// Risk tiers for tool calls — matches the design plan
const TOOL_TIERS = {
  // Read-only (neutral)
  Read: 'safe', Glob: 'safe', Grep: 'safe', WebFetch: 'safe', WebSearch: 'safe',
  // File modification (caution)
  Edit: 'modify', Write: 'modify', MultiEdit: 'modify', NotebookEdit: 'modify',
  // Delegation (elevated)
  Agent: 'delegate', Task: 'delegate', TaskCreate: 'delegate', TeamCreate: 'delegate',
  Skill: 'delegate',
  // Shell / dangerous (high)
  Bash: 'danger', Execute: 'danger',
}

const TIER_STYLES = {
  safe:     { border: '#71717a', bg: 'rgba(113,113,122,0.15)', text: '#a1a1aa' },
  modify:   { border: '#eab308', bg: 'rgba(234,179,8,0.12)',   text: '#fde68a' },
  delegate: { border: '#8b5cf6', bg: 'rgba(139,92,246,0.12)',  text: '#c4b5fd' },
  danger:   { border: '#f59e0b', bg: 'rgba(245,158,11,0.15)',  text: '#fcd34d' },
  unknown:  { border: '#f43f5e', bg: 'rgba(244,63,94,0.12)',   text: '#fda4af' },
}

const ROLE_STYLES = {
  user:      { border: '#6366f1', bg: 'rgba(99,102,241,0.12)',  text: '#a5b4fc', label: 'USER' },
  assistant: { border: '#10b981', bg: 'rgba(16,185,129,0.12)',  text: '#6ee7b7', label: 'ASSISTANT' },
  system:    { border: '#6b7280', bg: 'rgba(107,114,128,0.12)', text: '#9ca3af', label: 'SYSTEM' },
}

const VIOLATION_STYLE = { border: '#ef4444', bg: 'rgba(239,68,68,0.12)', text: '#fca5a5' }
const SUMMARY_STYLE   = { border: '#6b7280', bg: 'transparent',          text: '#6b7280' }

/** Extract just the filename from a path */
function basename(filepath) {
  if (!filepath) return ''
  const parts = String(filepath).split('/')
  return parts[parts.length - 1] || filepath
}

/** Build a concise one-line summary for a tool call */
function toolDetail(name, args) {
  if (!args) return null
  switch (name) {
    case 'Read':
    case 'Write':
    case 'NotebookEdit':
      return basename(args.file_path)
    case 'Edit':
    case 'MultiEdit':
      return basename(args.file_path)
    case 'Glob':
      return args.pattern || ''
    case 'Grep':
      return (args.pattern || '') + (args.path ? '  ' + basename(args.path) : '')
    case 'Bash':
      return truncate(args.command, 80)
    case 'Agent':
      return truncate(args.prompt || args.description, 60)
    case 'WebSearch':
      return args.query
    case 'WebFetch':
      return args.url
    case 'Skill':
      return args.skill
    case 'TaskCreate':
    case 'Task':
      return truncate(args.description || args.subject, 60)
    default: {
      // Generic: show first string argument
      const val = Object.values(args).find(v => typeof v === 'string' && v.length > 0)
      return val ? truncate(String(val), 60) : null
    }
  }
}

function truncate(text, max) {
  if (!text) return ''
  return text.length > max ? text.slice(0, max - 1) + '\u2026' : text
}

function TurnPill({ label, style }) {
  return (
    <span
      class="turn-pill"
      style={{
        background: style.bg,
        color: style.text,
        borderColor: style.border,
      }}
    >
      {label}
    </span>
  )
}

function TreeLine({ isLast }) {
  return (
    <span class="turn-tree">
      {isLast ? '\u2514\u2500' : '\u251C\u2500'}
    </span>
  )
}

export function SessionTurns({ sessionId }) {
  const [turns, setTurns] = useState(null)
  const [error, setError] = useState(false)

  useEffect(() => {
    if (!sessionId) return
    const controller = new AbortController()

    apiFetch('/control/sessions/' + sessionId + '/turns', { signal: controller.signal })
      .then(res => res.ok ? res.json() : null)
      .then(data => {
        if (!controller.signal.aborted) setTurns(data?.turns || [])
      })
      .catch(() => {
        if (!controller.signal.aborted) setError(true)
      })

    return () => controller.abort()
  }, [sessionId])

  if (error) return <div class="session-turns-empty">Could not load turns</div>
  if (turns === null) return <div class="session-turns-empty">Loading turns\u2026</div>
  if (turns.length === 0) return <div class="session-turns-empty">No turns recorded</div>

  return (
    <div class="session-turns">
      {turns.map((turn, i) => {
        const isLast = i === turns.length - 1

        if (turn.type === 'message') {
          const rs = ROLE_STYLES[turn.role] || ROLE_STYLES.system
          return (
            <div key={i} class="session-turn">
              <TreeLine isLast={isLast} />
              <span class="turn-time">{fmtTime(turn.timestamp)}</span>
              <TurnPill label={rs.label} style={rs} />
              {turn.backend && <span class="turn-meta">{turn.backend}</span>}
              {turn.content && <span class="turn-content">{truncate(turn.content, 120)}</span>}
            </div>
          )
        }

        if (turn.type === 'tool_call') {
          const tier = TOOL_TIERS[turn.tool_name] || 'unknown'
          const style = TIER_STYLES[tier]
          const detail = toolDetail(turn.tool_name, turn.arguments)
          return (
            <div key={i} class="session-turn">
              <TreeLine isLast={isLast} />
              <span class="turn-time">{fmtTime(turn.timestamp)}</span>
              <TurnPill label={turn.tool_name} style={style} />
              {detail && <span class="turn-detail">{detail}</span>}
            </div>
          )
        }

        if (turn.type === 'violation') {
          return (
            <div key={i} class="session-turn turn-violation-row">
              <TreeLine isLast={isLast} />
              <span class="turn-time">{fmtTime(turn.timestamp)}</span>
              <TurnPill
                label={'\u26A0 ' + (turn.severity || 'warning').toUpperCase()}
                style={VIOLATION_STYLE}
              />
              <span class="turn-content">
                {turn.rule_name}{turn.content ? ' \u2014 ' + truncate(turn.content, 80) : ''}
              </span>
            </div>
          )
        }

        if (turn.type === 'summary') {
          return (
            <div key={i} class="session-turn">
              <TreeLine isLast={isLast} />
              <span class="turn-time" />
              <TurnPill label="\u2026" style={SUMMARY_STYLE} />
              <span class="turn-content turn-summary-text">
                {turn.content || 'turns omitted'}
              </span>
            </div>
          )
        }

        return null
      })}
    </div>
  )
}

function fmtTime(ts) {
  if (!ts) return ''
  return new Date(ts).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })
}
