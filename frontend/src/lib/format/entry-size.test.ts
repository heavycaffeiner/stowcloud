import { beforeEach, describe, expect, it } from 'vitest'
import { setLocale } from '../i18n'
import { formatEntrySize } from './entry-size'

describe('formatEntrySize', () => {
  // The label goes through `t()`, and the locale defaults to Korean when
  // there is no localStorage to read a choice from, as in a test.
  beforeEach(() => {
    setLocale('en')
  })

  it('formats unencrypted files using formatBytes directly', () => {
    expect(formatEntrySize(1024)).toBe('1 KB')
    expect(formatEntrySize(75)).toBe('75 B')
  })

  it('derives plaintext size for encrypted files across block and header framing', () => {
    // 75 B ciphertext = 32-byte header/nonce + 16-byte Poly1305 tag + 27-byte plaintext
    expect(formatEntrySize(75, true)).toBe('27 B')
    // 32 B ciphertext = 32-byte header/nonce with 0 B plaintext
    expect(formatEntrySize(32, true)).toBe('0 B')
    // 65536 plaintext + 16 tag + 32 header = 65584 B ciphertext
    expect(formatEntrySize(65584, true)).toBe('64 KB')
  })

  it('labels size as encrypted when ciphertext cannot be derived', () => {
    // Shorter than 32-byte header
    expect(formatEntrySize(30, true)).toBe('30 B (encrypted)')
    // Remainder after header is 8 bytes, which is too short for a partial block (< 17 bytes)
    expect(formatEntrySize(40, true)).toBe('40 B (encrypted)')
  })
})
