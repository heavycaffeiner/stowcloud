import { useCallback, useRef } from 'react'
import type { Dispatch, SetStateAction } from 'react'
import { useStore } from 'zustand'
import { createStore, type StoreApi } from 'zustand/vanilla'

export function useComponentState<T>(initial: T | (() => T)): [T, Dispatch<SetStateAction<T>>] {
  const store = useRef<StoreApi<{ value: T }> | null>(null)
  if (store.current === null) {
    store.current = createStore(() => ({ value: typeof initial === 'function' ? (initial as () => T)() : initial }))
  }
  const value = useStore(store.current, (state) => state.value)
  const setValue = useCallback<Dispatch<SetStateAction<T>>>((next) => {
    store.current!.setState((state) => ({ value: typeof next === 'function' ? (next as (previous: T) => T)(state.value) : next }))
  }, [])
  return [value, setValue]
}
