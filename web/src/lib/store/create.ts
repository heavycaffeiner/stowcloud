import { createStore, type StoreApi } from 'zustand/vanilla'

export type Patch<S> = Partial<S> | ((state: S) => Partial<S>)

export interface StoreBase<S> {
  readonly api: StoreApi<S>
  getState(): S
  peek(): S
  subscribe(listener: (state: S, previousState: S) => void): () => void
  reset(): void
}

export function defineStore<S extends object, A extends object>(
  initial: S,
  actions: (set: (patch: Patch<S>) => void, get: () => S) => A
): StoreBase<S> & A {
  const api = createStore<S>(() => initial)
  const set = (patch: Patch<S>): void => {
    api.setState(patch)
  }

  return {
    ...actions(set, api.getState),
    api,
    getState: api.getState,
    peek: api.getState,
    subscribe: api.subscribe,
    reset: (): void => {
      api.setState(initial, true)
    }
  }
}
