import { describe, expect, it } from 'vitest'
import { safeReturnTo } from '../../../src/features/auth/hooks/use-login-flow'

describe('safeReturnTo', () => {
  it.each(['/b/docs', '/oauth/authorize?client_id=a&state=b', '/b/a%20b#top'])(
    'keeps the same-origin path %s',
    (raw) => {
      expect(safeReturnTo(raw)).toBe(raw)
    }
  )

  it.each([
    undefined,
    '',
    'b/docs',
    'https://evil.example/',
    '//evil.example/',
    '/\\evil.example/',
    '/\t/evil.example/',
    '/\n/evil.example/',
    '\\\\evil.example/'
  ])('refuses %j', (raw) => {
    expect(safeReturnTo(raw)).toBeNull()
  })
})
