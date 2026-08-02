import { describe, expect, it } from 'vitest'
import { BYTES_PER_MB, bytesToMb, formatBytes, formatEta, formatRate } from './bytes'

describe('formatBytes', () => {
  it('formats sub-1024 values in bytes', () => {
    expect(formatBytes(512)).toBe('512 B')
  })

  it('formats exactly 1024 as 1 KB', () => {
    expect(formatBytes(1024)).toBe('1 KB')
  })

  it('formats MB with one fraction digit by default', () => {
    expect(formatBytes(10 * 1024 * 1024)).toBe('10 MB')
    expect(formatBytes(1.5 * 1024 * 1024)).toBe('1.5 MB')
  })

  it('formats GB', () => {
    expect(formatBytes(3 * 1024 ** 3)).toBe('3 GB')
  })

  it('handles zero', () => {
    expect(formatBytes(0)).toBe('0 B')
  })

  it('handles negative sizes with a leading sign', () => {
    expect(formatBytes(-2048)).toBe('-2 KB')
  })

  it('never exceeds the largest known unit', () => {
    expect(formatBytes(1024 ** 8)).toMatch(/EB$/)
  })

  /** The whole point of one vocabulary: a size shown as "5 MB" and a field
   *  holding 5 MB have to be the same number of bytes. */
  it('agrees with the MB the settings inputs are denominated in', () => {
    expect(BYTES_PER_MB).toBe(1024 * 1024)
    expect(formatBytes(5 * BYTES_PER_MB)).toBe('5 MB')
    expect(bytesToMb(5 * BYTES_PER_MB)).toBe(5)
  })
})

describe('formatRate', () => {
  it('appends /s to a formatted byte value', () => {
    expect(formatRate(5 * 1024 * 1024)).toBe('5 MB/s')
  })

  it('returns an em dash for zero or invalid rates', () => {
    expect(formatRate(0)).toBe('—')
    expect(formatRate(Number.NaN)).toBe('—')
  })
})

describe('formatEta', () => {
  it('formats seconds only under a minute', () => {
    expect(formatEta(42)).toBe('42s')
  })

  it('formats minutes and seconds', () => {
    expect(formatEta(192)).toBe('3m 12s')
  })

  it('formats hours, minutes and seconds', () => {
    expect(formatEta(3725)).toBe('1h 2m 5s')
  })

  it('flags sub-second remaining time', () => {
    expect(formatEta(0.4)).toBe('<1s')
  })

  it('returns an em dash for invalid input', () => {
    expect(formatEta(-5)).toBe('—')
  })
})
