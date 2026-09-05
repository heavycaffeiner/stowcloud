// What is selected in the file browser, and where the keyboard cursor is.
//
// Selection is kept by name, not by row index: a refetch or a resort moves
// rows around, and a name still resolves afterwards. A name that is no longer
// listed simply matches nothing, so nothing has to prune it. The cursor is an
// index, because a row that has not loaded yet can still be focused while its
// page is on the way.
import { defineStore } from './create.svelte'

export interface SelectionState {
  readonly names: ReadonlySet<string>
  /** Where a shift-range measures from. */
  readonly anchor: string | null
  readonly focused: number | null
}

const EMPTY: SelectionState = { names: new Set<string>(), anchor: null, focused: null }

export const selection = defineStore(EMPTY, (set, get) => ({
  only(name: string, index: number | null = null): void {
    set({ names: new Set([name]), anchor: name, focused: index })
  },

  toggle(name: string, index: number | null = null): void {
    const names = new Set(get().names)
    if (!names.delete(name)) names.add(name)
    set({ names, anchor: name, focused: index })
  },

  /**
   * Shift-click and shift-arrow. Only rows currently loaded between the anchor
   * and the target can be named, so a range reaching into an unloaded gap
   * selects what is known; an anchor that is no longer listed degrades to a
   * plain single selection.
   */
  range(orderedNames: readonly string[], target: string): void {
    const { anchor } = get()
    const from = anchor === null ? -1 : orderedNames.indexOf(anchor)
    const to = orderedNames.indexOf(target)
    if (from === -1 || to === -1) {
      set({ names: new Set([target]), anchor: target, focused: to === -1 ? null : to })
      return
    }
    const [lo, hi] = from <= to ? [from, to] : [to, from]
    set({ names: new Set(orderedNames.slice(lo, hi + 1)), focused: to })
  },

  /** Everything currently loaded. A directory-wide select-all would need a
   *  bulk endpoint the server does not have. */
  all(orderedNames: readonly string[]): void {
    set({ names: new Set(orderedNames) })
  },

  /** Replaces the whole selection, for the rubber-band drag: shrinking the
   *  rectangle has to drop the rows it no longer covers. */
  replace(names: Iterable<string>): void {
    set({ names: new Set(names) })
  },

  clear(): void {
    set({ names: new Set<string>(), anchor: null })
  },

  focus(index: number | null): void {
    set({ focused: index })
  }
}))
