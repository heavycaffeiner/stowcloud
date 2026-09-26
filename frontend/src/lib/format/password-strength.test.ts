import { describe, expect, it } from 'vitest'
import { scorePasswordStrength } from './password-strength'

describe('scorePasswordStrength', () => {
  it('reports the empty string as weak with a zero ratio and no label', () => {
    const r = scorePasswordStrength('')
    expect(r.tier).toBe('weak')
    expect(r.ratio).toBe(0)
    expect(r.label).toBe('')
  })

  it('scores a short single-class password as weak', () => {
    expect(scorePasswordStrength('abcdefghi').tier).toBe('weak') // 9 chars, below the 10-char floor
    expect(scorePasswordStrength('abcdefghij').tier).toBe('weak') // 10 chars, one class
  })

  it('scores a longer password with some variety as fair', () => {
    expect(scorePasswordStrength('abcdefgh1234').tier).toBe('fair')
  })

  it('scores a long password with full character variety as strong', () => {
    expect(scorePasswordStrength('Correct-Horse-Battery-9!').tier).toBe('strong')
  })

  it('is monotonic: adding length never lowers the score', () => {
    const short = scorePasswordStrength('Abcd1234!')
    const longer = scorePasswordStrength('Abcd1234!Abcd1234!')
    expect(longer.score).toBeGreaterThanOrEqual(short.score)
  })

  it('keeps ratio within 0..1', () => {
    const r = scorePasswordStrength('Zz9!Zz9!Zz9!Zz9!Zz9!Zz9!')
    expect(r.ratio).toBeLessThanOrEqual(1)
    expect(r.ratio).toBeGreaterThan(0)
  })
})
