// The admin log screen's filter form.
//
// Only the form lives here. What the filters fetch is three queries keyed on
// the projection of this state (`query/logs.ts`), so a filter change is a new
// key rather than a request this store has to cancel and reconcile.
import { EMPTY_FILTERS, type LogFilters } from '../admin/log-view'
import { defineStore } from './create.svelte'

export interface LogsFormState {
  readonly filters: LogFilters
  /** Rows expanded to their full detail, by list key. */
  readonly expanded: ReadonlySet<string>
  /** Which timeline bar the keyboard is on, `null` until the reader moves it
   *  so the first Tab lands on the newest bucket. */
  readonly focusedBucket: number | null
}

const INITIAL: LogsFormState = { filters: EMPTY_FILTERS, expanded: new Set<string>(), focusedBucket: null }

export const logsForm = defineStore(INITIAL, (set, get) => ({
  /** A filter change drops the expansion set and the bar cursor: both name
   *  things that are about to be replaced. */
  patch(patch: Partial<LogFilters>): void {
    set({ filters: { ...get().filters, ...patch }, expanded: new Set<string>(), focusedBucket: null })
  },

  toggleLevel(level: string): void {
    const levels = new Set(get().filters.levels)
    if (!levels.delete(level)) levels.add(level)
    set({ filters: { ...get().filters, levels }, expanded: new Set<string>(), focusedBucket: null })
  },

  toggleExpanded(key: string): void {
    const expanded = new Set(get().expanded)
    if (!expanded.delete(key)) expanded.add(key)
    set({ expanded })
  },

  focusBucket(index: number | null): void {
    set({ focusedBucket: index })
  }
}))
