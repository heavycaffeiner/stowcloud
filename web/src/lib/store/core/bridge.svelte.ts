import type { StoreApi } from 'zustand/vanilla'

export interface RunesStoreHandle<S> {
  readonly current: S
}

/**
 * Connects a Zustand vanilla store to Svelte 5 runes within component lifecycles.
 *
 * It uses $state for local reactive tracking and $effect to maintain the subscription.
 * When the enclosing component unmounts, the $effect cleanup automatically cancels
 * the store subscription, preventing listener leaks.
 */
export function useRunesStore<T, S = T>(
  store: StoreApi<T>,
  selector: (state: T) => S = (s) => s as unknown as S
): RunesStoreHandle<S> {
  let state = $state<S>(selector(store.getState()))

  $effect(() => {
    state = selector(store.getState())
    const unsubscribe = store.subscribe((next) => {
      state = selector(next)
    })
    return () => {
      unsubscribe()
    }
  })

  return {
    get current() {
      return state
    }
  }
}
