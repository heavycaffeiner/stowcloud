// Theme and layout chrome. Persisted where a choice that resets on reload
// would be a choice the user has to make again on every reload.
import { defineStore } from './create.svelte'
import { readPref, writePref } from './persist'

export type ThemePref = 'system' | 'light' | 'dark'

/**
 * `unset` means the root drawer has never been opened or closed, so the
 * width-based default still applies. Without that third state the default was
 * re-applied on every load, and closing the drawer lasted until the next
 * refresh.
 */
export type DrawerPref = 'open' | 'closed' | 'unset'

const THEMES: readonly ThemePref[] = ['system', 'light', 'dark']
const DRAWERS: readonly DrawerPref[] = ['open', 'closed', 'unset']

/** MD3 window class breakpoint: rail versus bar plus drawer. */
export const COMPACT_MAX_PX = 905

export interface UiState {
  readonly theme: ThemePref
  readonly drawer: DrawerPref
  /** Unlike the drawer there is no width default to fall back to: the details
   *  panel is off until asked for, at any width. */
  readonly details: boolean
  readonly compact: boolean
}

export const ui = defineStore(
  {
    theme: readPref('sc.theme', THEMES, 'system'),
    drawer: readPref('sc.drawer', DRAWERS, 'unset'),
    details: readPref('sc.details', DRAWERS, 'closed') === 'open',
    compact: typeof window !== 'undefined' && window.innerWidth < COMPACT_MAX_PX
  } as UiState,
  (set) => ({
    setTheme(theme: ThemePref): void {
      set({ theme })
      writePref('sc.theme', theme)
    },
    setDrawer(open: boolean): void {
      set({ drawer: open ? 'open' : 'closed' })
      writePref('sc.drawer', open ? 'open' : 'closed')
    },
    setDetails(open: boolean): void {
      set({ details: open })
      writePref('sc.details', open ? 'open' : 'closed')
    },
    setCompact(compact: boolean): void {
      set({ compact })
    }
  })
)
