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

import { tp, setLocale } from './index'

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
