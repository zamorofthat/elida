import { describe, it, expect, afterEach } from 'vitest'
import { render, cleanup } from '@testing-library/preact'
import { SessionTurns, turnAnchorId } from './SessionTurns'

const turns = [
  { type: 'message', role: 'user', content: 'hi', timestamp: '2026-10-20T09:00:00Z' },
  { type: 'violation', rule_name: 'injection_elevated', severity: 'warning', event_id: 'ev_1', timestamp: '2026-10-20T09:01:00Z' },
  { type: 'violation', rule_name: 'no_event_id', severity: 'warning', timestamp: '2026-10-20T09:02:00Z' },
  { type: 'tool_call', tool_name: 'Read', arguments: { file_path: '/a/b.txt' }, timestamp: '2026-10-20T09:03:00Z' },
]

describe('SessionTurns anchors', () => {
  afterEach(() => cleanup())

  it('gives a violation turn the id turn-<event_id>', () => {
    const { container } = render(<SessionTurns preloadedTurns={turns} />)
    const row = container.querySelector('[id="turn-ev_1"]')
    expect(row).toBeTruthy()
    expect(row.className).toContain('turn-violation-row')
  })

  it('emits no id for a violation without an event_id or for any other turn type', () => {
    const { container } = render(<SessionTurns preloadedTurns={turns} />)
    expect([...container.querySelectorAll('[id]')].map((e) => e.id)).toEqual(['turn-ev_1'])
  })

  it('turnAnchorId prefixes the event id and yields undefined for none', () => {
    expect(turnAnchorId('ev_9')).toBe('turn-ev_9')
    expect(turnAnchorId('')).toBeUndefined()
    expect(turnAnchorId(undefined)).toBeUndefined()
  })
})
