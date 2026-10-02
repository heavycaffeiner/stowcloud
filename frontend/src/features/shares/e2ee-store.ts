// The codec announces unlock and lock as window events. This mirrors them in a signal, so a render that
// asks whether a share is unlocked runs again when the answer can change.
import { signal } from '@preact/signals-react'
import { isUnlocked } from '../../lib/crypto/e2ee'

const keyChanges = signal(0)
const bump = (): void => {
  keyChanges.value += 1
}
window.addEventListener('sc:unlock', bump)
window.addEventListener('sc:lock', bump)

/** Whether this session holds the key for `salt`. The caller re-renders on unlock and lock. */
export function useShareUnlocked(salt: string | undefined): boolean {
  void keyChanges.value
  return salt !== undefined && isUnlocked(salt)
}
