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
      signal: expect.any(AbortSignal),
    })
  })

  it('omits Authorization header when no key in localStorage', async () => {
    mockFetch.mockResolvedValue({ status: 200 })

    await apiFetch('/api/test')

    expect(mockFetch).toHaveBeenCalledWith('/api/test', {
      headers: {},
      signal: expect.any(AbortSignal),
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
      signal: expect.any(AbortSignal),
    })
  })
})

describe('apiFetch timeout', () => {
  let mockFetch

  // A fetch that never answers on its own but, like the real one, rejects
  // with the signal's reason when its signal aborts.
  const hanging = (url, opts) => new Promise((_, reject) => {
    const abort = () => reject(opts.signal.reason)
    if (opts.signal.aborted) return abort()
    opts.signal.addEventListener('abort', abort)
  })

  beforeEach(() => {
    vi.useFakeTimers()
    vi.stubGlobal('localStorage', {
      getItem: vi.fn(() => null),
      setItem: vi.fn(),
      removeItem: vi.fn(),
    })
    mockFetch = vi.fn(hanging)
    vi.stubGlobal('fetch', mockFetch)
    setLogoutHandler(() => {})
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    vi.useRealTimers()
  })

  it('rejects with Timeout after timeoutMs and aborts the underlying fetch', async () => {
    const settled = expect(apiFetch('/api/slow', { timeoutMs: 1000 })).rejects.toThrow('Timeout')
    await vi.advanceTimersByTimeAsync(1000)
    await settled
    expect(mockFetch.mock.calls[0][1].signal.aborted).toBe(true)
  })

  it('defaults to 15 seconds', async () => {
    let outcome = 'pending'
    apiFetch('/api/slow').then(() => { outcome = 'ok' }, (e) => { outcome = e.message })
    await vi.advanceTimersByTimeAsync(14999)
    expect(outcome).toBe('pending')
    await vi.advanceTimersByTimeAsync(1)
    expect(outcome).toBe('Timeout')
  })

  it('never times out when timeoutMs is 0', async () => {
    let outcome = 'pending'
    apiFetch('/api/slow', { timeoutMs: 0 }).then(() => { outcome = 'ok' }, (e) => { outcome = e.message })
    await vi.advanceTimersByTimeAsync(10 * 60 * 1000)
    expect(outcome).toBe('pending')
    expect(vi.getTimerCount()).toBe(0)
  })

  it('does not forward timeoutMs or the caller signal to fetch', async () => {
    const caller = new AbortController()
    mockFetch.mockResolvedValue({ status: 200 })
    await apiFetch('/api/test', { timeoutMs: 5000, signal: caller.signal, method: 'POST' })
    const sent = mockFetch.mock.calls[0][1]
    expect(sent).not.toHaveProperty('timeoutMs')
    expect(sent.signal).not.toBe(caller.signal)
    expect(sent.method).toBe('POST')
  })

  it('still rejects with the caller abort reason, not Timeout, when the caller aborts', async () => {
    const caller = new AbortController()
    const settled = expect(apiFetch('/api/slow', { signal: caller.signal, timeoutMs: 1000 }))
      .rejects.toMatchObject({ name: 'AbortError' })
    caller.abort()
    await settled
    expect(vi.getTimerCount()).toBe(0)
  })

  it('rejects immediately with the abort reason when the caller signal is already aborted', async () => {
    const caller = new AbortController()
    caller.abort()
    await expect(apiFetch('/api/slow', { signal: caller.signal })).rejects.toMatchObject({ name: 'AbortError' })
    expect(vi.getTimerCount()).toBe(0)
  })

  it('clears the timer once the response arrives', async () => {
    mockFetch.mockResolvedValue({ status: 200 })
    await apiFetch('/api/test')
    expect(vi.getTimerCount()).toBe(0)
  })

  it('keeps the 401 path: clears the key, logs out and throws Unauthorized, with no timer left', async () => {
    const onLogout = vi.fn()
    setLogoutHandler(onLogout)
    mockFetch.mockResolvedValue({ status: 401 })
    await expect(apiFetch('/api/test')).rejects.toThrow('Unauthorized')
    expect(localStorage.removeItem).toHaveBeenCalledWith(AUTH_KEY)
    expect(onLogout).toHaveBeenCalled()
    expect(vi.getTimerCount()).toBe(0)
  })
})
