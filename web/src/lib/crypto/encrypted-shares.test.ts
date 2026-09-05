// web/src/lib/crypto/encrypted-shares.test.ts
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import {
  encryptedShares,
  encryptionForLabel,
  invalidateEncryptedShares,
  setEncryptedSharesSource,
  shareLabelOf
} from './encrypted-shares'

// The module reads through an installed fetcher rather than importing the
// api layer, so a test installs one directly instead of mocking a module.
const shareEncryptionList = vi.fn()

beforeEach(() => {
  shareEncryptionList.mockReset()
  setEncryptedSharesSource(() => shareEncryptionList().then((r: { shares: unknown[] }) => r.shares))
  invalidateEncryptedShares()
})

afterEach(() => {
  invalidateEncryptedShares()
})

describe('shareLabelOf', () => {
  it('takes the first segment of a leading-slash vpath', () => {
    expect(shareLabelOf('/vault/reports/q3.pdf')).toBe('vault')
  })

  it('takes the first segment when there is no leading slash', () => {
    expect(shareLabelOf('vault/reports/q3.pdf')).toBe('vault')
  })

  it('returns the whole string for a share root with nothing after it', () => {
    expect(shareLabelOf('/vault')).toBe('vault')
    expect(shareLabelOf('vault')).toBe('vault')
  })
})

describe('encryptedShares (fetch-once cache)', () => {
  it('fetches the set once and reuses it for concurrent and later calls', async () => {
    const row = { share: 1, labels: ['vault'], scheme: 'rclone-crypt-v1', salt: 'x'.repeat(22), verifier: 'abc', createdNs: 1 }
    shareEncryptionList.mockResolvedValue({ shares: [row] })

    const [a, b] = await Promise.all([encryptedShares(), encryptedShares()])
    await encryptedShares()

    expect(a).toEqual([row])
    expect(b).toEqual([row])
    expect(shareEncryptionList).toHaveBeenCalledTimes(1)
  })

  it('does not cache a rejected fetch, so the next call tries again', async () => {
    shareEncryptionList.mockRejectedValueOnce(new Error('network down'))
    await expect(encryptedShares()).rejects.toThrow('network down')

    shareEncryptionList.mockResolvedValueOnce({ shares: [] })
    await expect(encryptedShares()).resolves.toEqual([])
    expect(shareEncryptionList).toHaveBeenCalledTimes(2)
  })

  it('invalidateEncryptedShares forces the next call to re-fetch', async () => {
    shareEncryptionList.mockResolvedValue({ shares: [] })
    await encryptedShares()
    invalidateEncryptedShares()
    await encryptedShares()
    expect(shareEncryptionList).toHaveBeenCalledTimes(2)
  })
})

describe('encryptionForLabel', () => {
  it('finds the row whose labels include the given label', async () => {
    const row = { share: 7, labels: ['team-a', 'team-a-drop'], scheme: 'rclone-crypt-v1', salt: 'x'.repeat(22), verifier: 'abc', createdNs: 1 }
    shareEncryptionList.mockResolvedValue({ shares: [row] })

    expect(await encryptionForLabel('team-a-drop')).toEqual(row)
  })

  it('resolves to null once a successful fetch confirms the label is unencrypted', async () => {
    shareEncryptionList.mockResolvedValue({ shares: [] })
    expect(await encryptionForLabel('plain-vault')).toBeNull()
  })

  it('fails closed: propagates a fetch failure rather than resolving to unencrypted', async () => {
    shareEncryptionList.mockRejectedValue(new Error('network down'))
    await expect(encryptionForLabel('vault')).rejects.toThrow('network down')
  })
})
