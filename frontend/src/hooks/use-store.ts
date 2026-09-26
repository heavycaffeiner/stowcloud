import { useStore as useZustandStore } from 'zustand'
import type { StoreBase } from '../lib/store/create'

export function useStore<S, T>(store: StoreBase<S>, selector: (state: S) => T): T {
  return useZustandStore(store.api, selector)
}
