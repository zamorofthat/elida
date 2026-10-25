import { describe, it, expect, afterEach } from 'vitest'
import { render, cleanup } from '@testing-library/preact'
import { TimelineBand, mergeTimeline, MARKER_COLORS } from './TimelineBand'

const turns = [
  { type: 'message', timestamp: '2026-10-20T09:00:00Z', role: 'user' },
  { type: 'tool_call', timestamp: '2026-10-20T09:02:00Z', tool_name: 'read_note' },
  { type: 'violation', timestamp: '2026-10-20T09:04:00Z', rule_name: 'correlation.slow_burn_injection' },
]
const markers = [
  { type: 'semantic_decision', timestamp: '2026-10-20T09:01:00Z', title: 'injection_elevated 0.40' },
  { type: 'correlation_match', timestamp: '2026-10-20T09:04:00Z', title: 'slow_burn_injection (3 events)' },
  { type: 'shadow_match', timestamp: '2026-10-20T09:04:30Z', title: 'shadow slow_burn_injection' },
]

describe('TimelineBand', () => {
  afterEach(() => cleanup())

  it('merges turns and markers in time order, turns first on ties', () => {
    const merged = mergeTimeline(turns, markers)
    expect(merged.map((m) => m.type)).toEqual(['message', 'semantic_decision', 'tool_call', 'violation', 'correlation_match', 'shadow_match'])
  })

  it('renders one dot per item with the marker colour and title', () => {
    const { container } = render(<TimelineBand turns={turns} markers={markers} />)
    const dots = container.querySelectorAll('.timeline-dot')
    expect(dots.length).toBe(6)
    expect(dots[1].getAttribute('title')).toContain('injection_elevated')
    expect(dots[1].className).toContain('timeline-dot-semantic_decision')
    expect(dots[4].getAttribute('title')).toContain('slow_burn_injection')
    expect(dots[4].className).toContain('timeline-dot-correlation_match')
    expect(MARKER_COLORS.correlation_match).toBe('#ec4899')
  })

  it('renders nothing without turns or markers, and turns alone as before', () => {
    expect(render(<TimelineBand turns={[]} markers={[]} />).container.querySelector('.timeline-band')).toBeNull()
    cleanup()
    const { container } = render(<TimelineBand turns={turns} />)
    expect(container.querySelectorAll('.timeline-dot').length).toBe(3)
  })
})
