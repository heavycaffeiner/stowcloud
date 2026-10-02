// How the key store follows the codec, and what it tells subscribers on each change.
import { act, renderHook } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { deriveKeys, generateSalt, isUnlocked, lock, makeVerifier } from '../../../src/lib/crypto/e2ee'
import {
  type KeyChange,
  unlockShare,
  useE2eeStore,
  useKeyChange,
  useShareUnlocked
} from '../../../src/features/shares/e2ee-store'

const PASSPHRASE = 'correct horse battery staple'

async function share(): Promise<{ salt: string; verifier: string }> {
  const salt = generateSalt()
  return { salt, verifier: await makeVerifier(await deriveKeys(PASSPHRASE, salt)) }
}

afterEach(() => {
  act(() => lock())
})

describe('e2ee store', () => {
  it('holds the unlocked salt until the codec locks', async () => {
    const { salt, verifier } = await share()
    const { result } = renderHook(() => useShareUnlocked(salt))
    expect(result.current).toBe(false)

    await act(() => unlockShare(PASSPHRASE, salt, verifier))
    expect(useE2eeStore.getState().unlockedSalt).toBe(salt)
    expect(result.current).toBe(true)

    act(() => lock())
    expect(useE2eeStore.getState().unlockedSalt).toBeNull()
    expect(result.current).toBe(false)
  })

  it('announces before-lock while the key still works, then lock and unlock', async () => {
    const first = await share()
    const second = await share()
    await act(() => unlockShare(PASSPHRASE, first.salt, first.verifier))
    const seen: Array<KeyChange & { keyHeld: boolean }> = []
    renderHook(() => useKeyChange((change) => seen.push({ ...change, keyHeld: isUnlocked(first.salt) })))

    await act(() => unlockShare(PASSPHRASE, second.salt, second.verifier))
    expect(seen).toEqual([
      { kind: 'before-lock', salt: first.salt, keyHeld: true },
      { kind: 'lock', keyHeld: false },
      { kind: 'unlock', salt: second.salt, keyHeld: false }
    ])
  })

  it('ignores events the codec does not back', async () => {
    const { salt, verifier } = await share()
    const seen: KeyChange[] = []
    const { unmount } = renderHook(() => useKeyChange((change) => seen.push(change)))

    act(() => {
      window.dispatchEvent(new CustomEvent('sc:unlock', { detail: { salt } }))
      window.dispatchEvent(new CustomEvent('sc:unlock', { detail: { salt: 42 } }))
    })
    expect(useE2eeStore.getState().unlockedSalt).toBeNull()

    await act(() => unlockShare(PASSPHRASE, salt, verifier))
    seen.length = 0
    act(() => {
      window.dispatchEvent(new CustomEvent('sc:lock'))
      window.dispatchEvent(new CustomEvent('sc:before-lock', { detail: { salt: 'another' } }))
    })
    expect(useE2eeStore.getState().unlockedSalt).toBe(salt)
    expect(seen).toEqual([])

    unmount()
    act(() => lock())
    expect(seen).toEqual([])
  })
})
