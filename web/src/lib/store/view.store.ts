// How the file browser is laid out and ordered. All three outlive the page:
// a toggle that resets on reload is one the user has to re-set on reload.
import type { Order, SortKey } from '../api/types'
import { defineStore } from './create.svelte'
import { readPref, writePref } from './persist'

export type ViewMode = 'list' | 'grid'
export type Density = 'compact' | 'comfortable' | 'spacious'

const VIEWS: readonly ViewMode[] = ['list', 'grid']
const DENSITIES: readonly Density[] = ['compact', 'comfortable', 'spacious']
const SORT_KEYS: readonly SortKey[] = ['name', 'size', 'mtime', 'kind']
const ORDERS: readonly Order[] = ['asc', 'desc']

export interface ViewState {
  readonly mode: ViewMode
  readonly density: Density
  readonly sortKey: SortKey
  readonly sortOrder: Order
}

export const view = defineStore(
  {
    mode: readPref('sc.view', VIEWS, 'list'),
    density: readPref('sc.density', DENSITIES, 'comfortable'),
    sortKey: readPref('sc.sort', SORT_KEYS, 'name'),
    sortOrder: readPref('sc.order', ORDERS, 'asc')
  } as ViewState,
  (set) => ({
    setMode(mode: ViewMode): void {
      set({ mode })
      writePref('sc.view', mode)
    },
    setDensity(density: Density): void {
      set({ density })
      writePref('sc.density', density)
    },
    setSort(sortKey: SortKey, sortOrder: Order): void {
      set({ sortKey, sortOrder })
      writePref('sc.sort', sortKey)
      writePref('sc.order', sortOrder)
    }
  })
)
