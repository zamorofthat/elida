// Timeline band: one dot per turn and per evidence marker, in time order.
// Turns are the conversation (/turns); markers are semantic decisions,
// correlation matches and shadow entries from the flagged/session record.

export const MARKER_COLORS = {
  message: '#6366f1',
  tool_call: '#f59e0b',
  violation: '#ef4444',
  summary: '#6b7280',
  semantic_decision: '#a855f7',
  correlation_match: '#ec4899',
  shadow_decision: '#9ca3af',
  shadow_match: '#9ca3af',
}

function ms(ts) {
  const n = ts ? new Date(ts).getTime() : NaN
  return Number.isNaN(n) ? 0 : n
}

// mergeTimeline returns turns and markers sorted by timestamp. The sort is
// stable and turns are listed before markers, so a marker that shares a
// turn's timestamp follows it.
export function mergeTimeline(turns, markers) {
  const items = [
    ...(turns || []).map((t) => ({ ...t, _order: 0 })),
    ...(markers || []).map((m) => ({ ...m, _order: 1 })),
  ]
  return items
    .map((it, i) => ({ it, i }))
    .sort((a, b) => ms(a.it.timestamp) - ms(b.it.timestamp) || a.it._order - b.it._order || a.i - b.i)
    .map(({ it }) => it)
}

function dotTitle(t) {
  if (t.title) return t.title
  return `${t.type}${t.tool_name ? ': ' + t.tool_name : ''}${t.role ? ' (' + t.role + ')' : ''}`
}

export function TimelineBand({ turns, markers }) {
  const items = mergeTimeline(turns, markers)
  if (items.length === 0) return null
  return (
    <div class="timeline-band">
      {items.map((t, i) => (
        <span
          key={i}
          class={'timeline-dot timeline-dot-' + t.type}
          style={{ background: MARKER_COLORS[t.type] || '#6b7280' }}
          title={dotTitle(t)}
        />
      ))}
    </div>
  )
}
