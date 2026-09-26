import { describe, expect, it } from 'vitest'
import { cleanupKey, resumeKey } from './idb'

describe('upload IDB keys', () => {
  const base = {
    accountId: 'account-a',
    sessionContext: 'session-a',
    dest: '/Files',
    relativePath: 'folder/item.bin'
  }

  it('partitions identical source metadata by account, session, destination, and path', () => {
    const key = resumeKey('item.bin', 12, 34, base)
    expect(resumeKey('item.bin', 12, 34, { ...base, accountId: 'account-b' })).not.toBe(key)
    expect(resumeKey('item.bin', 12, 34, { ...base, sessionContext: 'session-b' })).not.toBe(key)
    expect(resumeKey('item.bin', 12, 34, { ...base, dest: '/Other' })).not.toBe(key)
    expect(resumeKey('item.bin', 12, 34, { ...base, relativePath: 'other/item.bin' })).not.toBe(key)
  })

  it('names cleanup records by session rather than resumable file metadata', () => {
    expect(cleanupKey('sess-1')).toBe('v1:sess-1')
    expect(cleanupKey('sess-1')).not.toBe(cleanupKey('sess-2'))
  })
})
