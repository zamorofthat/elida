import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { apiFetch, AUTH_KEY, setLogoutHandler } from './apiFetch'

describe('apiFetch', () => {
  let mockFetch
  let storage

  beforeEach(() => {
    storage = {}
    vi.stubGlobal('localStorage', {
      getItem: vi.fn((k) => storage[k] ?? null),
      setItem: vi.fn((k, v) => { storage[k] = v }),
      removeItem: vi.fn((k) => { delete storage[k] }),
    })
    mockFetch = vi.fn()
    vi.stubGlobal('fetch', mockFetch)
    setLogoutHandler(() => {})
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('adds Authorization header when key exists in localStorage', async () => {
    storage[AUTH_KEY] = 'my-secret'
    mockFetch.mockResolvedValue({ status: 200 })

    await apiFetch('/api/test')

    expect(mockFetch).toHaveBeenCalledWith('/api/test', {
      headers: { Authorization: 'Bearer my-secret' },
    })
  })

  it('omits Authorization header when no key in localStorage', async () => {
    mockFetch.mockResolvedValue({ status: 200 })

    await apiFetch('/api/test')

    expect(mockFetch).toHaveBeenCalledWith('/api/test', {
      headers: {},
    })
  })

  it('clears localStorage and calls logout handler on 401', async () => {
    storage[AUTH_KEY] = 'bad-key'
    const onLogout = vi.fn()
    setLogoutHandler(onLogout)
    mockFetch.mockResolvedValue({ status: 401 })

    await expect(apiFetch('/api/test')).rejects.toThrow('Unauthorized')

    expect(localStorage.removeItem).toHaveBeenCalledWith(AUTH_KEY)
    expect(onLogout).toHaveBeenCalled()
  })

  it('returns response on non-401 status', async () => {
    const fakeRes = { status: 200, json: () => ({ ok: true }) }
    mockFetch.mockResolvedValue(fakeRes)

    const res = await apiFetch('/api/test')

    expect(res).toBe(fakeRes)
  })

  it('merges custom headers with auth header', async () => {
    storage[AUTH_KEY] = 'key123'
    mockFetch.mockResolvedValue({ status: 200 })

    await apiFetch('/api/test', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
    })

    expect(mockFetch).toHaveBeenCalledWith('/api/test', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        Authorization: 'Bearer key123',
      },
    })
  })
})
