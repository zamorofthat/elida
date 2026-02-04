import { describe, it, expect } from 'vitest'
import { formatDuration, formatDurationStr, formatBytes, truncateId } from './utils'

describe('formatDuration', () => {
  it('returns dash for 0', () => {
    expect(formatDuration(0)).toBe('-')
  })

  it('returns dash for null/undefined', () => {
    expect(formatDuration(null)).toBe('-')
    expect(formatDuration(undefined)).toBe('-')
  })

  it('formats milliseconds', () => {
    expect(formatDuration(50)).toBe('50ms')
    expect(formatDuration(999)).toBe('999ms')
  })

  it('formats seconds', () => {
    expect(formatDuration(1000)).toBe('1.0s')
    expect(formatDuration(5500)).toBe('5.5s')
    expect(formatDuration(59999)).toBe('60.0s')
  })

  it('formats minutes and seconds', () => {
    expect(formatDuration(60000)).toBe('1m 0s')
    expect(formatDuration(90000)).toBe('1m 30s')
    expect(formatDuration(3661000)).toBe('61m 1s')
  })

  it('formats hours as minutes', () => {
    expect(formatDuration(7200000)).toBe('120m 0s')
  })
})

describe('formatDurationStr', () => {
  it('returns dash for null/undefined/empty', () => {
    expect(formatDurationStr(null)).toBe('-')
    expect(formatDurationStr(undefined)).toBe('-')
    expect(formatDurationStr('')).toBe('-')
  })

  it('parses Go duration with minutes and seconds', () => {
    expect(formatDurationStr('1m5.912904792s')).toBe('1m 5s')
  })

  it('parses hours and minutes', () => {
    expect(formatDurationStr('2h30m')).toBe('2h 30m')
  })

  it('parses milliseconds', () => {
    expect(formatDurationStr('500ms')).toBe('500ms')
  })

  it('parses seconds', () => {
    expect(formatDurationStr('45s')).toBe('45s')
    expect(formatDurationStr('45.123s')).toBe('45s')
  })

  it('returns original string for unrecognized format', () => {
    expect(formatDurationStr('bogus')).toBe('bogus')
  })
})

describe('formatBytes', () => {
  it('formats 0 bytes', () => {
    expect(formatBytes(0)).toBe('0 B')
  })

  it('formats bytes', () => {
    expect(formatBytes(500)).toBe('500 B')
    expect(formatBytes(1)).toBe('1 B')
  })

  it('formats kilobytes', () => {
    expect(formatBytes(1024)).toBe('1 KB')
    expect(formatBytes(1536)).toBe('1.5 KB')
  })

  it('formats megabytes', () => {
    expect(formatBytes(1048576)).toBe('1 MB')
    expect(formatBytes(2621440)).toBe('2.5 MB')
  })

  it('formats gigabytes', () => {
    expect(formatBytes(1073741824)).toBe('1 GB')
  })
})

describe('truncateId', () => {
  it('returns dash for null/undefined', () => {
    expect(truncateId(null)).toBe('-')
    expect(truncateId(undefined)).toBe('-')
  })

  it('truncates long IDs with default length', () => {
    expect(truncateId('abcdefghijklmnop')).toBe('abcdefgh...')
  })

  it('truncates with custom length', () => {
    expect(truncateId('abcdefghijklmnop', 4)).toBe('abcd...')
    expect(truncateId('abcdefghijklmnop', 12)).toBe('abcdefghijkl...')
  })

  it('still appends ellipsis for short IDs', () => {
    expect(truncateId('abc')).toBe('abc...')
    expect(truncateId('ab', 4)).toBe('ab...')
  })
})
