import { useState, useEffect, useRef } from 'preact/hooks'
import { formatBytes, formatDuration, formatDurationStr, truncateId } from '../utils'
import { StateBadge } from './shared/Badge'
import { SessionTurns } from './SessionTurns'

function DurationTicker({ startTime, durationMs, duration, state }) {
  const [elapsed, setElapsed] = useState(null)

  useEffect(() => {
    if (state !== 'active' || !startTime) {
      setElapsed(null)
      return
    }
    const update = () => {
      const ms = Date.now() - new Date(startTime).getTime()
      setElapsed(ms)
    }
    update()
    const id = setInterval(update, 1000)
    return () => clearInterval(id)
  }, [startTime, state])

  if (elapsed !== null) return <span class="mono duration">{formatDuration(elapsed)}</span>
  if (durationMs) return <span class="mono duration">{formatDuration(durationMs)}</span>
  return <span class="mono duration">{formatDurationStr(duration)}</span>
}

export function SessionRow({ session, onKill }) {
  const [expanded, setExpanded] = useState(false)

  const formatBackends = (s) => {
    if (s.backends_used && Object.keys(s.backends_used).length > 0) {
      return Object.entries(s.backends_used)
        .map(([name, count]) => count > 1 ? `${name}(${count})` : name)
        .join(', ')
    }
    try {
      return new URL(s.backend).host
    } catch {
      return s.backend || '-'
    }
  }

  const riskScore = session.risk_score || 0
  const riskClass = riskScore >= 30 ? 'risk-high' : riskScore >= 10 ? 'risk-med' : ''

  return (
    <div class={'session-row-wrapper' + (expanded ? ' expanded' : '')}>
      <div
        class={'session-row' + (riskClass ? ' ' + riskClass : '')}
        onClick={() => setExpanded(!expanded)}
      >
        <div class="session-row-left">
          <span class="session-row-expand">{expanded ? '\u25BE' : '\u25B8'}</span>
          {session.state === 'active' && <span class="live-dot" />}
          <span class="mono session-row-id">{truncateId(session.id)}</span>
          <StateBadge state={session.state} />
        </div>

        <div class="session-row-center">
          <span class="mono muted session-row-backends">{formatBackends(session)}</span>
          <span class="session-row-reqs">{session.request_count} req</span>
          <span class="mono session-row-bytes">
            {formatBytes(session.bytes_in)} / {formatBytes(session.bytes_out)}
          </span>
        </div>

        <div class="session-row-right">
          <DurationTicker
            startTime={session.start_time}
            durationMs={session.duration_ms}
            duration={session.duration}
            state={session.state}
          />
          {riskScore > 0 && (
            <span class={'session-row-risk ' + riskClass}>
              {riskScore}
            </span>
          )}
          {session.state === 'active' && (
            <button
              class="btn btn-danger btn-sm"
              onClick={(e) => { e.stopPropagation(); onKill(session.id); }}
            >
              Kill
            </button>
          )}
        </div>
      </div>

      {expanded && (
        <div class="session-row-detail">
          <SessionTurns sessionId={session.id} />
        </div>
      )}
    </div>
  )
}
