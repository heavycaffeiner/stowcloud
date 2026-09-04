import { createStore, type StoreApi } from 'zustand/vanilla'
import { setAdd, setClear, setDelete, setIntersect, setToggle } from '../core/fp'

export interface SelectionState {
  readonly selected: ReadonlySet<string>
}

export type SelectionAction =
  | { type: 'SELECT_ONLY'; name: string }
  | { type: 'TOGGLE'; name: string }
  | { type: 'SELECT_RANGE'; orderedNames: readonly string[]; anchor: string; target: string }
  | { type: 'SELECT_ALL'; orderedNames: readonly string[] }
  | { type: 'CLEAR' }
  | { type: 'RECONCILE'; stillPresent: ReadonlySet<string> }

export function pureSelectOnly(_current: ReadonlySet<string>, name: string): ReadonlySet<string> {
  return new Set([name])
}

export function pureToggle(current: ReadonlySet<string>, name: string): ReadonlySet<string> {
  return setToggle(current, name)
}

export function pureSelectRange(
  _current: ReadonlySet<string>,
  orderedNames: readonly string[],
  anchor: string,
  target: string
): ReadonlySet<string> {
  const ai = orderedNames.indexOf(anchor)
  const ti = orderedNames.indexOf(target)
  if (ai === -1 || ti === -1) {
    return new Set([target])
  }
  const [lo, hi] = ai <= ti ? [ai, ti] : [ti, ai]
  return new Set(orderedNames.slice(lo, hi + 1))
}

export function pureSelectAll(orderedNames: readonly string[]): ReadonlySet<string> {
  return new Set(orderedNames)
}

export function pureClear(): ReadonlySet<string> {
  return setClear<string>()
}

export function pureReconcile(
  current: ReadonlySet<string>,
  stillPresent: ReadonlySet<string>
): ReadonlySet<string> {
  return setIntersect(current, stillPresent)
}

export function selectionReducer(
  state: SelectionState,
  action: SelectionAction
): SelectionState {
  switch (action.type) {
    case 'SELECT_ONLY':
      return { selected: pureSelectOnly(state.selected, action.name) }
    case 'TOGGLE':
      return { selected: pureToggle(state.selected, action.name) }
    case 'SELECT_RANGE':
      return {
        selected: pureSelectRange(state.selected, action.orderedNames, action.anchor, action.target)
      }
    case 'SELECT_ALL':
      return { selected: pureSelectAll(action.orderedNames) }
    case 'CLEAR':
      return { selected: pureClear() }
    case 'RECONCILE':
      return { selected: pureReconcile(state.selected, action.stillPresent) }
    default:
      return state
  }
}

export interface SelectionStore extends StoreApi<SelectionState> {
  dispatch(action: SelectionAction): void
}

export function createSelectionStore(initial = new Set<string>()): SelectionStore {
  const store = createStore<SelectionState>(() => ({
    selected: initial
  }))

  return {
    ...store,
    dispatch(action: SelectionAction): void {
      store.setState((prev) => selectionReducer(prev, action))
    }
  }
}
