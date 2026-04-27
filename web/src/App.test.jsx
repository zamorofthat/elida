import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, waitFor, cleanup } from '@testing-library/preact'
import { App } from './App'
import { AUTH_KEY } from './apiFetch'

describe('App auth gating', () => {
  let mockFetch
  let storage

  beforeEach(() => {
    storage = {}
    vi.stubGlobal('localStorage', {
      getItem: vi.fn((k) => storage[k] ?? null),
      setItem: vi.fn((k, v) => { storage[k] = String(v) }),
      removeItem: vi.fn((k) => { delete storage[k] }),
    })
    mockFetch = vi.fn()
    vi.stubGlobal('fetch', mockFetch)
  })

  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it('skips login when server has no auth configured (health 200)', async () => {
    mockFetch.mockResolvedValue({ status: 200, ok: true, json: () => Promise.resolve({}) })

    const { container } = render(<App />)

    await waitFor(() => {
      expect(container.querySelector('.topnav')).toBeTruthy()
    })
    expect(container.querySelector('.login-container')).toBeNull()
  })

  it('shows login when server requires auth (health 401)', async () => {
    mockFetch.mockResolvedValue({ status: 401, ok: false })

    const { container } = render(<App />)

    await waitFor(() => {
      expect(container.querySelector('.login-container')).toBeTruthy()
    })
    expect(container.querySelector('.topnav')).toBeNull()
  })

  it('skips probe and renders dashboard when key exists in localStorage', async () => {
    storage[AUTH_KEY] = 'existing-key'
    mockFetch.mockResolvedValue({ status: 200, ok: true, json: () => Promise.resolve({}) })

    const { container } = render(<App />)

    await waitFor(() => {
      expect(container.querySelector('.topnav')).toBeTruthy()
    })
    // Should not have probed health without credentials
    const healthCalls = mockFetch.mock.calls.filter(
      ([url]) => url === '/control/health' && !mockFetch.mock.calls[0]?.[1]?.headers?.Authorization
    )
    // The first fetch should be an authed call (stats etc.), not an unauthed health probe
    expect(container.querySelector('.login-container')).toBeNull()
  })
})
