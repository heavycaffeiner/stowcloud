// The codec holds at most one share key and announces each change as a window event. This module is the
// only listener: it mirrors the codec in a store for rendering and passes each change on to subscribers.
import { useEffect, useEffectEvent } from 'react'
import { create } from 'zustand'
import { isUnlocked, unlock } from '../../lib/crypto/e2ee'

export interface E2eeState {
  /** The salt of the share whose key this session holds, or null while locked. */
  readonly unlockedSalt: string | null
}

export type KeyChange =
  | { readonly kind: 'before-lock'; readonly salt: string }
  | { readonly kind: 'lock' }
  | { readonly kind: 'unlock'; readonly salt: string }

export const useE2eeStore = create<E2eeState>()(() => ({ unlockedSalt: null }))

const subscribers = new Set<(change: KeyChange) => void>()

function announce(change: KeyChange): void {
  for (const subscriber of subscribers) {
    try {
      subscriber(change)
    } catch (error) {
      reportError(error)
    }
  }
}

function saltOf(event: Event): string | null {
  const salt: unknown = event instanceof CustomEvent ? (event.detail as { salt?: unknown } | null)?.salt : undefined
  return typeof salt === 'string' ? salt : null
}

// Any script can dispatch these events, so each one is checked against the codec before it counts.
window.addEventListener('sc:before-lock', (event) => {
  const salt = saltOf(event)
  if (salt !== null && isUnlocked(salt)) announce({ kind: 'before-lock', salt })
})
window.addEventListener('sc:lock', () => {
  if (isUnlocked()) return
  useE2eeStore.setState({ unlockedSalt: null })
  announce({ kind: 'lock' })
})
window.addEventListener('sc:unlock', (event) => {
  const salt = saltOf(event)
  if (salt === null || !isUnlocked(salt)) return
  useE2eeStore.setState({ unlockedSalt: salt })
  announce({ kind: 'unlock', salt })
})

/** Unlocks a share. Going through this module means the store is listening before the key exists. */
export function unlockShare(passphrase: string, salt: string, verifier: string): Promise<void> {
  return unlock(passphrase, salt, verifier)
}

/** Whether this session holds the key for `salt`. */
export function useShareUnlocked(salt: string | undefined): boolean {
  return useE2eeStore((state) => salt !== undefined && state.unlockedSalt === salt)
}

/** Calls `onChange` for each key change while mounted. A before-lock change runs while the key still works. */
export function useKeyChange(onChange: (change: KeyChange) => void): void {
  const handle = useEffectEvent(onChange)
  useEffect(() => {
    const subscriber = (change: KeyChange): void => handle(change)
    subscribers.add(subscriber)
    return () => {
      subscribers.delete(subscriber)
    }
  }, [])
}
