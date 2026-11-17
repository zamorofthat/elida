import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { killSessionRequest } from './killSession'

describe('killSessionRequest', () => {
  let fetchMock
  beforeEach(() => {
    fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
  })
  afterEach(() => vi.unstubAllGlobals())

  it('sends no body when the server default is wanted', async () => {
    fetchMock.mockResolvedValue({ status: 200, ok: true })
    await killSessionRequest('s1', '')
    const [url, opts] = fetchMock.mock.calls[0]
    expect(url).toBe('/control/sessions/s1/kill')
    expect(opts.method).toBe('POST')
    expect(opts.body).toBeUndefined()
  })

  it('sends the block as JSON', async () => {
    fetchMock.mockResolvedValue({ status: 200, ok: true })
    await killSessionRequest('s1', '30m')
    const [, opts] = fetchMock.mock.calls[0]
    expect(JSON.parse(opts.body)).toEqual({ block: '30m' })
    expect(opts.headers['Content-Type']).toBe('application/json')
  })

  it('rejects with the server message when the kill did not happen', async () => {
    fetchMock.mockResolvedValue({ status: 400, ok: false, text: () => Promise.resolve('block "x": want permanent\n') })
    await expect(killSessionRequest('s1', 'x')).rejects.toThrow('block "x": want permanent')
  })

  it('rejects on a 404 with no message', async () => {
    fetchMock.mockResolvedValue({ status: 404, ok: false })
    await expect(killSessionRequest('s1', '')).rejects.toThrow('Kill failed (404)')
  })

  it('encodes the session id', async () => {
    fetchMock.mockResolvedValue({ status: 200, ok: true })
    await killSessionRequest('a/b c', '')
    expect(fetchMock.mock.calls[0][0]).toBe('/control/sessions/a%2Fb%20c/kill')
  })
})
