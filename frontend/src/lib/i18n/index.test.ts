import { describe, expect, it, vi, beforeEach } from 'vitest'

vi.mock('./en.json', () => ({
  default: {
    'test.items_one': '{count} item',
    'test.items_other': '{count} items',
    'test.job_finished_one': '{kind} job finished. {count} item processed.',
    'test.job_finished_other': '{kind} job finished. {count} items processed.',
  },
}))

vi.mock('./ko.json', () => ({
  default: {
    'test.items_one': '항목 {count}개',
    'test.items_other': '항목 {count}개',
    'test.job_finished_one': '{kind} 작업 완료. {count}개 항목 처리됨.',
    'test.job_finished_other': '{kind} 작업 완료. {count}개 항목 처리됨.',
  },
}))

import { formatModifiedDateNs, tp, setLocale } from './index'

beforeEach(() => {
  setLocale('en')
})

describe('tp', () => {
  it('selects _one when count is 1', () => {
    expect(tp('test.items', 1)).toBe('1 item')
  })

  it('selects _other when count is 0', () => {
    expect(tp('test.items', 0)).toBe('0 items')
  })

  it('selects _other when count is many', () => {
    expect(tp('test.items', 5)).toBe('5 items')
    expect(tp('test.items', 42)).toBe('42 items')
  })

  it('interpolates additional params alongside count', () => {
    expect(tp('test.job_finished', 1, { kind: 'Copy' })).toBe(
      'Copy job finished. 1 item processed.',
    )
    expect(tp('test.job_finished', 3, { kind: 'Copy' })).toBe(
      'Copy job finished. 3 items processed.',
    )
  })

  it('allows overriding count in params', () => {
    expect(tp('test.items', 1, { count: 'one' })).toBe('one item')
  })

  it('works when switching to Korean locale', () => {
    setLocale('ko')
    expect(tp('test.items', 1)).toBe('항목 1개')
    expect(tp('test.items', 0)).toBe('항목 0개')
    expect(tp('test.items', 5)).toBe('항목 5개')
    expect(tp('test.job_finished', 1, { kind: '복사' })).toBe(
      '복사 작업 완료. 1개 항목 처리됨.',
    )
  })
})

describe('formatModifiedDateNs', () => {
  const nanoseconds = (date: Date) => (BigInt(date.getTime()) * 1_000_000n).toString()

  it('keeps day before month and 24-hour time in both languages', () => {
    const stamp = nanoseconds(new Date(2026, 8, 17, 18, 4, 59))
    expect(formatModifiedDateNs(stamp)).toBe('2026-17-09 18:04')
    setLocale('ko')
    expect(formatModifiedDateNs(stamp)).toBe('2026-17-09 18:04')
  })

  it('pads single digits and renders midnight without a day-period label', () => {
    expect(formatModifiedDateNs(nanoseconds(new Date(2026, 0, 2, 0, 5)))).toBe('2026-02-01 00:05')
  })

  it('rejects malformed or out-of-range timestamps', () => {
    expect(() => formatModifiedDateNs('not-a-time')).toThrow()
    expect(() => formatModifiedDateNs('999999999999999999999999999999')).toThrow(RangeError)
  })
})
