import { useState } from 'preact/hooks'

// The kill button, with the choice of how long the session stays blocked made
// in place: clicking Kill opens the choice next to the button instead of a
// browser pop-up, and nothing is sent until Kill is confirmed.

const CHOICES = [
  { value: 'default', label: 'Default' },
  { value: '5m', label: '5 minutes' },
  { value: '30m', label: '30 minutes' },
  { value: '1h', label: '1 hour' },
  { value: '24h', label: '24 hours' },
  { value: 'custom', label: 'Custom…' },
  { value: 'permanent', label: 'Permanent' },
]

// blockFor turns the form into the API value: '' for the server's configured
// block, 'permanent', or a duration. It returns null for a custom length that
// is not a whole number of minutes of at least 1.
export function blockFor(choice, minutes) {
  if (choice === 'default') return ''
  if (choice === 'custom') {
    const n = Number(minutes)
    return Number.isInteger(n) && n >= 1 ? n + 'm' : null
  }
  return choice
}

// killBlockLabel says how a killed session is blocked, or '' when it is not.
export function killBlockLabel(session) {
  if (!session || session.state !== 'killed' || !session.kill_block) return ''
  if (session.kill_block === 'permanent') return 'blocked permanently'
  if (!session.blocked_until) return ''
  const until = new Date(session.blocked_until)
  if (Number.isNaN(until.getTime())) return ''
  const time = until.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  const sameDay = until.toDateString() === new Date().toDateString()
  return 'blocked until ' + (sameDay ? time : until.toLocaleDateString([], { month: 'short', day: 'numeric' }) + ' ' + time)
}

// onKill(block) must reject if the kill failed; its message is shown inline.
export function KillControl({ onKill, size = 'sm' }) {
  const [open, setOpen] = useState(false)
  const [choice, setChoice] = useState('default')
  const [minutes, setMinutes] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  // The controls sit in rows and cells that open something when clicked.
  const stop = (e) => e.stopPropagation()

  const close = () => { setOpen(false); setError('') }

  if (!open) {
    return (
      <button class={'btn btn-danger btn-' + size} onClick={(e) => { stop(e); setOpen(true) }}>
        Kill
      </button>
    )
  }

  const block = blockFor(choice, minutes)
  const confirm = async () => {
    if (block === null || busy) return
    setBusy(true)
    setError('')
    try {
      await onKill(block)
      setBusy(false)
      setOpen(false)
    } catch (err) {
      setBusy(false)
      setError(err && err.message ? err.message : 'Kill failed')
    }
  }

  return (
    <span
      class="kill-control"
      role="group"
      aria-label="Kill session"
      onClick={stop}
      onKeyDown={(e) => { if (e.key === 'Escape') { e.stopPropagation(); close() } }}
    >
      <label class="kill-control-field">
        <span class="kill-control-label">Block for</span>
        <select
          class="kill-control-select"
          value={choice}
          disabled={busy}
          title="Default uses the server's kill_block setting"
          onChange={(e) => setChoice(e.currentTarget.value)}
        >
          {CHOICES.map(c => <option key={c.value} value={c.value}>{c.label}</option>)}
        </select>
      </label>
      {choice === 'custom' && (
        <label class="kill-control-field">
          <input
            class="kill-control-minutes"
            type="number"
            min="1"
            step="1"
            placeholder="30"
            aria-label="Minutes"
            value={minutes}
            disabled={busy}
            onInput={(e) => setMinutes(e.currentTarget.value)}
          />
          <span class="kill-control-label">min</span>
        </label>
      )}
      <button
        class="btn btn-danger btn-xs"
        disabled={busy || block === null}
        onClick={confirm}
      >
        {busy ? 'Killing…' : 'Kill'}
      </button>
      <button class="btn btn-secondary btn-xs" disabled={busy} onClick={close}>Cancel</button>
      {error && <span class="kill-control-error" role="alert">{error}</span>}
    </span>
  )
}
