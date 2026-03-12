import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, fireEvent, waitFor, cleanup } from '@testing-library/preact'
import { Login } from './App'
import { AUTH_KEY } from './apiFetch'

describe('Login', () => {
  let mockFetch
  let storage

  beforeEach(() => {
    storage = {}
    vi.stubGlobal('localStorage', {
      getItem: (k) => storage[k] ?? null,
      setItem: (k, v) => { storage[k] = String(v) },
      removeItem: (k) => { delete storage[k] },
      clear: () => { storage = {} },
    })
    mockFetch = vi.fn()
    vi.stubGlobal('fetch', mockFetch)
  })

  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it('renders input and submit button', () => {
    const { getByPlaceholderText, getByText } = render(<Login onLogin={() => {}} />)
    expect(getByPlaceholderText('API key')).toBeTruthy()
    expect(getByText('Sign In')).toBeTruthy()
  })

  it('disables submit button when input is empty', () => {
    const { getByText } = render(<Login onLogin={() => {}} />)
    const btn = getByText('Sign In')
    expect(btn.disabled).toBe(true)
  })

  it('calls onLogin and stores key on valid key (200)', async () => {
    const onLogin = vi.fn()
    mockFetch.mockResolvedValue({ status: 200, ok: true })

    const { getByPlaceholderText, getByText } = render(<Login onLogin={onLogin} />)
    const input = getByPlaceholderText('API key')

    fireEvent.input(input, { target: { value: 'good-key' } })
    fireEvent.submit(getByText('Sign In').closest('form'))

    await waitFor(() => {
      expect(onLogin).toHaveBeenCalled()
    })
    expect(storage[AUTH_KEY]).toBe('good-key')
  })

  it('shows error on invalid key (401)', async () => {
    mockFetch.mockResolvedValue({ status: 401, ok: false })

    const { getByPlaceholderText, getByText, findByText } = render(<Login onLogin={() => {}} />)
    fireEvent.input(getByPlaceholderText('API key'), { target: { value: 'bad-key' } })
    fireEvent.submit(getByText('Sign In').closest('form'))

    expect(await findByText('Invalid API key')).toBeTruthy()
  })

  it('shows connection error when server is unreachable', async () => {
    mockFetch.mockRejectedValue(new TypeError('Failed to fetch'))

    const { getByPlaceholderText, getByText, findByText } = render(<Login onLogin={() => {}} />)
    fireEvent.input(getByPlaceholderText('API key'), { target: { value: 'any-key' } })
    fireEvent.submit(getByText('Sign In').closest('form'))

    expect(await findByText('Cannot reach server')).toBeTruthy()
  })
})
