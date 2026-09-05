// The one bridge between zustand's vanilla store and Svelte 5 runes.
//
// Every store in this directory is built with `defineStore`: immutable state,
// a flat set of actions, and a `.state` getter components read directly. No
// per-component subscription boilerplate, no classes, no `$effect`.
import { createStore } from 'zustand/vanilla'

/** Applied to the current state; the return value is merged over it. */
export type Patch<S> = Partial<S> | ((state: S) => Partial<S>)

export interface StoreBase<S> {
  /** Reactive snapshot. Replaced wholesale on every write, never mutated. */
  readonly state: S
  /** Non-reactive read, for code outside a reactive scope. */
  peek(): S
  /** Back to `initial`. Only tests and logout need this. */
  reset(): void
}

/**
 * Builds a store from its initial state and a factory for its actions.
 *
 * `set` merges a partial (or the result of an updater) into the state, so an
 * action never has to spread what it is not changing. State is held with
 * `$state.raw`: zustand replaces the object on every write, so a deep proxy
 * would cost allocation on read for reactivity that identity already gives.
 */
export function defineStore<S extends object, A extends object>(
  initial: S,
  actions: (set: (patch: Patch<S>) => void, get: () => S) => A
): StoreBase<S> & A {
  const api = createStore<S>(() => initial)
  let snapshot = $state.raw(initial)
  api.subscribe((next) => {
    snapshot = next
  })

  const set = (patch: Patch<S>): void => {
    api.setState(patch as Partial<S>)
  }

  // An object literal, not `Object.assign`: assign copies a getter by reading
  // it once, which froze `.state` at the initial value and made every store
  // silently non-reactive.
  return {
    ...actions(set, api.getState),
    get state(): S {
      return snapshot
    },
    peek: api.getState,
    reset: (): void => {
      api.setState(initial, true)
    }
  }
}
