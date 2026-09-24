import { useCallback, useRef } from 'react'
import { useStore } from 'zustand'
import { createStore, type StoreApi } from 'zustand/vanilla'

export type RoutePatch<S extends object> = Partial<S> | ((state: S) => Partial<S>)

/** Creates a Zustand store per mounted route instance, never a module singleton. */
export function useRouteStore<S extends object>(initial: S | (() => S)): [S, (patch: RoutePatch<S>) => void] {
  const storeRef = useRef<StoreApi<S> | null>(null)
  if (storeRef.current === null) storeRef.current = createStore(() => typeof initial === 'function' ? (initial as () => S)() : initial)
  const state = useStore(storeRef.current)
  const patch = useCallback((next: RoutePatch<S>) => {
    storeRef.current!.setState((current) => ({ ...current, ...(typeof next === 'function' ? next(current) : next) }))
  }, [])
  return [state, patch]
}
