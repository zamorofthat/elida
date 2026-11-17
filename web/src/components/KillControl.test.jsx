import { describe, it, expect, vi, afterEach } from 'vitest'
import { render, cleanup, fireEvent, waitFor } from '@testing-library/preact'
import { KillControl, blockFor, killBlockLabel } from './KillControl'
import { SessionRow } from './SessionRow'

afterEach(cleanup)

describe('blockFor', () => {
  it('maps each choice to the value the API takes', () => {
    expect(blockFor('default', '')).toBe('')
    expect(blockFor('permanent', '')).toBe('permanent')
    expect(blockFor('30m', '')).toBe('30m')
    expect(blockFor('1h', '')).toBe('1h')
    expect(blockFor('custom', '45')).toBe('45m')
  })

  it('refuses a custom length that is not a whole number of minutes', () => {
    for (const bad of ['', '0', '-5', '1.5', 'abc']) expect(blockFor('custom', bad)).toBeNull()
  })
})

describe('killBlockLabel', () => {
  it('says nothing for a session that is not killed or carries no block', () => {
    expect(killBlockLabel({ state: 'active', kill_block: 'permanent' })).toBe('')
    expect(killBlockLabel({ state: 'killed' })).toBe('')
    expect(killBlockLabel(null)).toBe('')
  })

  it('says permanent, or until when', () => {
    expect(killBlockLabel({ state: 'killed', kill_block: 'permanent' })).toBe('blocked permanently')
    const soon = new Date(Date.now() + 10 * 60 * 1000).toISOString()
    expect(killBlockLabel({ state: 'killed', kill_block: 'duration', blocked_until: soon })).toMatch(/^blocked until /)
  })
})

describe('KillControl', () => {
  it('opens the choice in place and sends nothing until Kill is confirmed', () => {
    const onKill = vi.fn().mockResolvedValue()
    const { getByText, queryByRole, getByRole } = render(<KillControl onKill={onKill} />)
    expect(queryByRole('group')).toBeNull()

    fireEvent.click(getByText('Kill'))

    expect(getByRole('group')).toBeTruthy()
    expect(onKill).not.toHaveBeenCalled()
  })

  it('sends the configured block (empty) by default', async () => {
    const onKill = vi.fn().mockResolvedValue()
    const { getByText, getByRole } = render(<KillControl onKill={onKill} />)
    fireEvent.click(getByText('Kill'))
    fireEvent.click(getByRole('group').querySelector('button.btn-danger'))
    await waitFor(() => expect(onKill).toHaveBeenCalledWith(''))
  })

  it('sends permanent when that is chosen', async () => {
    const onKill = vi.fn().mockResolvedValue()
    const { getByText, getByRole } = render(<KillControl onKill={onKill} />)
    fireEvent.click(getByText('Kill'))
    const group = getByRole('group')
    fireEvent.change(group.querySelector('select'), { target: { value: 'permanent' } })
    fireEvent.click(group.querySelector('button.btn-danger'))
    await waitFor(() => expect(onKill).toHaveBeenCalledWith('permanent'))
  })

  it('sends a custom number of minutes, and holds Kill back until it is valid', async () => {
    const onKill = vi.fn().mockResolvedValue()
    const { getByText, getByRole, getByLabelText } = render(<KillControl onKill={onKill} />)
    fireEvent.click(getByText('Kill'))
    const group = getByRole('group')
    fireEvent.change(group.querySelector('select'), { target: { value: 'custom' } })
    const confirm = group.querySelector('button.btn-danger')
    expect(confirm.disabled).toBe(true)

    fireEvent.input(getByLabelText('Minutes'), { target: { value: '45' } })
    expect(confirm.disabled).toBe(false)
    fireEvent.click(confirm)
    await waitFor(() => expect(onKill).toHaveBeenCalledWith('45m'))
  })

  it('cancels without sending, on Cancel and on Escape', () => {
    const onKill = vi.fn()
    const { getByText, getByRole, queryByRole } = render(<KillControl onKill={onKill} />)
    fireEvent.click(getByText('Kill'))
    fireEvent.click(getByText('Cancel'))
    expect(queryByRole('group')).toBeNull()

    fireEvent.click(getByText('Kill'))
    fireEvent.keyDown(getByRole('group'), { key: 'Escape' })
    expect(queryByRole('group')).toBeNull()
    expect(onKill).not.toHaveBeenCalled()
  })

  it('shows a failure next to the button and stays open for another try', async () => {
    const onKill = vi.fn().mockRejectedValue(new Error('Session not found or already terminated'))
    const { getByText, getByRole, findByRole } = render(<KillControl onKill={onKill} />)
    fireEvent.click(getByText('Kill'))
    fireEvent.click(getByRole('group').querySelector('button.btn-danger'))

    const alert = await findByRole('alert')
    expect(alert.textContent).toContain('already terminated')
    expect(getByRole('group')).toBeTruthy()
  })

  it('closes after a kill that succeeds', async () => {
    const onKill = vi.fn().mockResolvedValue()
    const { getByText, getByRole, queryByRole } = render(<KillControl onKill={onKill} />)
    fireEvent.click(getByText('Kill'))
    fireEvent.click(getByRole('group').querySelector('button.btn-danger'))
    await waitFor(() => expect(queryByRole('group')).toBeNull())
  })

  it('does not trigger the row it sits in', () => {
    const onRow = vi.fn()
    const { getByText, getByRole } = render(
      <div onClick={onRow}><KillControl onKill={vi.fn().mockResolvedValue()} /></div>
    )
    fireEvent.click(getByText('Kill'))
    fireEvent.click(getByRole('group'))
    fireEvent.change(getByRole('group').querySelector('select'), { target: { value: '1h' } })
    expect(onRow).not.toHaveBeenCalled()
  })
})

describe('SessionRow', () => {
  const live = { id: 'sess-1', state: 'active', request_count: 2, bytes_in: 1, bytes_out: 1, start_time: new Date().toISOString() }

  it('kills from an inline choice, with no browser dialog', async () => {
    const confirmSpy = vi.spyOn(window, 'confirm')
    const onKill = vi.fn().mockResolvedValue()
    const { getByText, getByRole } = render(<SessionRow session={live} onKill={onKill} onViewDetail={() => {}} />)

    fireEvent.click(getByText('Kill'))
    fireEvent.change(getByRole('group').querySelector('select'), { target: { value: 'permanent' } })
    fireEvent.click(getByRole('group').querySelector('button.btn-danger'))

    await waitFor(() => expect(onKill).toHaveBeenCalledWith('sess-1', 'permanent'))
    expect(confirmSpy).not.toHaveBeenCalled()
  })

  it('shows how a killed session is blocked', () => {
    const killed = { ...live, state: 'killed', kill_block: 'permanent' }
    const { getByText } = render(<SessionRow session={killed} onKill={() => {}} onViewDetail={() => {}} />)
    expect(getByText('blocked permanently')).toBeTruthy()
  })
})
