export const AUTH_KEY = 'elida_api_key'

let _onLogout = () => {}

export function setLogoutHandler(fn) {
  _onLogout = fn
}

const DEFAULT_TIMEOUT_MS = 15000

// The timer covers the time until the response headers arrive: it is cleared
// as soon as fetch settles, so a long-running body read is not cut off.
export async function apiFetch(url, opts = {}) {
  const { timeoutMs = DEFAULT_TIMEOUT_MS, signal: callerSignal, ...rest } = opts
  const key = localStorage.getItem(AUTH_KEY)
  const headers = { ...rest.headers }
  if (key) {
    headers['Authorization'] = 'Bearer ' + key
  }

  // The fetch is driven by an internal controller that either the timer or
  // the caller's signal can abort (AbortSignal.any is newer than the
  // supported browsers).
  const controller = new AbortController()
  const relay = () => controller.abort(callerSignal.reason)
  if (callerSignal) {
    if (callerSignal.aborted) relay()
    else callerSignal.addEventListener('abort', relay, { once: true })
  }

  let timer
  const timeout = new Promise((_, reject) => {
    if (timeoutMs > 0) {
      timer = setTimeout(() => {
        // Reject before aborting: aborting makes fetch reject too, and the
        // race must settle on Timeout, not on the AbortError that follows.
        reject(new Error('Timeout'))
        controller.abort()
      }, timeoutMs)
    }
  })

  let res
  try {
    res = await Promise.race([
      fetch(url, { ...rest, headers, signal: controller.signal }),
      timeout,
    ])
  } finally {
    clearTimeout(timer)
    callerSignal?.removeEventListener('abort', relay)
  }
  if (res.status === 401) {
    localStorage.removeItem(AUTH_KEY)
    _onLogout()
    throw new Error('Unauthorized')
  }
  return res
}
