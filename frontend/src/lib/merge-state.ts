export type StatePatch<S> = Partial<S> | ((state: S) => Partial<S>)

// A useReducer reducer for object state updated a few fields at a time. A
// function patch runs inside the reducer, so it must be pure.
export function mergeState<S extends object>(state: S, patch: StatePatch<S>): S {
  return { ...state, ...(typeof patch === 'function' ? patch(state) : patch) }
}
