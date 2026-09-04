import type { StoreApi } from 'zustand/vanilla'

export interface ReadableStore<T> {
  subscribe(fn: (value: T) => void): () => void
}

/**
 * Wraps a Zustand vanilla store into Svelte's readable store contract.
 *
 * Svelte requires subscribe(fn) to synchronously invoke fn with the current value
 * before returning an unsubscribe handle. Zustand vanilla store.subscribe only fires
 * on subsequent updates, so this adapter ensures the initial state is delivered immediately.
 */
export function toReadableStore<T, S = T>(
  store: StoreApi<T>,
  selector: (state: T) => S = (s) => s as unknown as S
): ReadableStore<S> {
  return {
    subscribe(fn: (value: S) => void): () => void {
      fn(selector(store.getState()))
      return store.subscribe((next) => {
        fn(selector(next))
      })
    }
  }
}
