import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, waitFor, cleanup, fireEvent } from '@testing-library/preact'
import { App } from './App'

// /control/voice answers 503 when the WebSocket transport is off, and 200 with
// an empty list when the transport is up but tracking is off. Neither can ever
// produce a row, so the dashboard should not ask at all.
describe('App voice capability', () => {
  let mockFetch
  let storage

  const respond = (capabilities) => (url) => {
    if (url.startsWith('/control/stats')) {
      return Promise.resolve({
        status: 200, ok: true,
        json: () => Promise.resolve({ total: 0, active: 0, capabilities }),
      })
    }
    if (url.startsWith('/control/voice')) {
      return Promise.resolve({ status: 503, ok: false, json: () => Promise.resolve({}) })
    }
    return Promise.resolve({ status: 200, ok: true, json: () => Promise.resolve({}) })
  }

  const calledVoice = () =>
    mockFetch.mock.calls.filter(([url]) => String(url).startsWith('/control/voice'))

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

  it('hides the Voice tab when ELIDA reports voice is unavailable', async () => {
    mockFetch.mockImplementation(respond({ voice: { enabled: false, protocols: [] } }))

    const { container } = render(<App />)

    await waitFor(() => expect(container.querySelector('.topnav')).toBeTruthy())
    await waitFor(() => {
      const tabs = [...container.querySelectorAll('.topnav-item')].map((b) => b.textContent.trim())
      expect(tabs).not.toContain('Voice')
    })
    expect(calledVoice()).toHaveLength(0)
  })

  it('shows the Voice tab when voice is available', async () => {
    mockFetch.mockImplementation(
      respond({ voice: { enabled: true, protocols: ['deepgram', 'elevenlabs'] } }),
    )

    const { container } = render(<App />)

    await waitFor(() => {
      const tabs = [...container.querySelectorAll('.topnav-item')].map((b) => b.textContent.trim())
      expect(tabs).toContain('Voice')
    })
  })

  // The case the redirect exists for: voice was available, the reader opened
  // the tab, and ELIDA came back without it. Hiding the tab alone would strand
  // them on a view the nav no longer offers, still polling a dead endpoint.
  it('leaves the voice view when voice goes away', async () => {
    let capabilities = { voice: { enabled: true, protocols: ['elevenlabs'] } }
    mockFetch.mockImplementation((url) => respond(capabilities)(url))

    const { container } = render(<App />)

    const voiceTab = await waitFor(() => {
      const tab = [...container.querySelectorAll('.topnav-item')]
        .find((b) => b.textContent.trim() === 'Voice')
      expect(tab).toBeTruthy()
      return tab
    })

    fireEvent.click(voiceTab)
    await waitFor(() => expect(container.textContent).toContain('Voice Sessions'))

    // ELIDA restarts with voice switched off.
    capabilities = { voice: { enabled: false, protocols: [] } }

    await waitFor(() => {
      const tabs = [...container.querySelectorAll('.topnav-item')].map((b) => b.textContent.trim())
      expect(tabs).not.toContain('Voice')
    }, { timeout: 10000 })
    // Not merely de-navved: the voice view itself is gone.
    expect(container.textContent).not.toContain('Voice Sessions')
  }, 15000)
})
