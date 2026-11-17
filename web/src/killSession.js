import { apiFetch } from './apiFetch'

// POST /control/sessions/{id}/kill. `block` is how long requests for the
// session are refused afterwards: 'permanent', 'until_hour_change', a duration
// such as '30m', or empty for the server's configured kill_block. Rejects
// with the server's message when the kill did not happen, so the caller can
// show it where the person clicked.
export async function killSessionRequest(id, block) {
  const opts = { method: 'POST' }
  if (block) {
    opts.headers = { 'Content-Type': 'application/json' }
    opts.body = JSON.stringify({ block })
  }
  const res = await apiFetch('/control/sessions/' + encodeURIComponent(id) + '/kill', opts)
  if (!res.ok) {
    const text = typeof res.text === 'function' ? (await res.text()).trim() : ''
    throw new Error(text || 'Kill failed (' + res.status + ')')
  }
}
