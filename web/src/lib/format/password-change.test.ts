import { describe, expect, it } from 'vitest'
import { validatePasswordChange } from './password-change'

describe('validatePasswordChange', () => {
  it('reports the minimum it measured a short password against', () => {
    expect(validatePasswordChange('short', 'short', 10)).toEqual({ kind: 'too_short', min: 10 })
  })

  it('reports a confirmation that does not match', () => {
    expect(validatePasswordChange('longenoughpass', 'differentpass', 10)).toEqual({ kind: 'mismatch' })
  })

  it('accepts a long enough password that matches its confirmation', () => {
    expect(validatePasswordChange('longenoughpass', 'longenoughpass', 10)).toBeNull()
  })

  it('checks length before matching, so a short pair is short rather than mismatched', () => {
    expect(validatePasswordChange('abc', 'abc', 10)).toEqual({ kind: 'too_short', min: 10 })
  })
})
