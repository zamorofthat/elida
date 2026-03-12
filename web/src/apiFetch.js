export const AUTH_KEY = 'elida_api_key'

let _onLogout = () => {}

export function setLogoutHandler(fn) {
  _onLogout = fn
}

export async function apiFetch(url, opts = {}) {
  const key = localStorage.getItem(AUTH_KEY)
  const headers = { ...opts.headers }
  if (key) {
    headers['Authorization'] = 'Bearer ' + key
  }
  const res = await fetch(url, { ...opts, headers })
  if (res.status === 401) {
    localStorage.removeItem(AUTH_KEY)
    _onLogout()
    throw new Error('Unauthorized')
  }
  return res
}
