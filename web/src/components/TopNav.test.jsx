import { describe, it, expect, afterEach } from 'vitest'
import { render, cleanup } from '@testing-library/preact'
import { TopNav } from './TopNav'

const labels = (container) =>
  [...container.querySelectorAll('.topnav-item')].map((b) => b.textContent.trim())

describe('TopNav voice tab', () => {
  afterEach(cleanup)

  it('offers Voice when ELIDA reports voice is available', () => {
    const { container } = render(<TopNav activePage="dashboard" onNavigate={() => {}} voiceEnabled />)

    expect(labels(container)).toContain('Voice')
  })

  it('omits Voice when voice is unavailable, so the tab is not a dead end', () => {
    const { container } = render(<TopNav activePage="dashboard" onNavigate={() => {}} voiceEnabled={false} />)

    expect(labels(container)).not.toContain('Voice')
  })

  it('omits Voice before the capability is known rather than flashing a dead tab', () => {
    const { container } = render(<TopNav activePage="dashboard" onNavigate={() => {}} />)

    expect(labels(container)).not.toContain('Voice')
  })

  it('leaves every other tab in place when voice is hidden', () => {
    const { container } = render(<TopNav activePage="dashboard" onNavigate={() => {}} voiceEnabled={false} />)

    expect(labels(container)).toEqual([
      'Dashboard', 'Sessions', 'Tool Use', 'Flagged', 'History', 'Settings',
    ])
  })
})
