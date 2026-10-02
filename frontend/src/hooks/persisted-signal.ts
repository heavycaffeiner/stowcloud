// A signal backed by localStorage, for the preferences that outlive a page.
//
// Every read and write is guarded: private mode and a full quota both throw on
// plain access, and a preference is never worth failing a render over.
import { signal, type Signal } from '@preact/signals-react'

function read<T extends string>(key: string, allowed: readonly T[], fallback: T): T {
  try {
    const saved = localStorage.getItem(key)
    if (allowed.includes(saved as T)) return saved as T
  } catch {
    /* unavailable */
  }
  return fallback
}

export function persistedSignal<T extends string>(key: string, allowed: readonly T[], fallback: T): Signal<T> {
  const value = signal(read(key, allowed, fallback))
  value.subscribe((next) => {
    try {
      localStorage.setItem(key, next)
    } catch {
      /* unavailable */
    }
  })
  return value
}
