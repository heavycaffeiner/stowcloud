// Theme and layout chrome. Persisted where a choice that resets on reload
// would be a choice the user has to make again on every reload.
import { defineStore } from './create'
import { readPref, writePref } from './persist'

export type ThemePref = 'system' | 'light' | 'dark'

const THEMES: readonly ThemePref[] = ['system', 'light', 'dark']
const DETAILS = ['open', 'closed'] as const

/** The existing compact breakpoint shared by navigation and file controls. */
export const COMPACT_MAX_PX = 905

export interface UiState {
  readonly theme: ThemePref
  /** Unlike navigation there is no width default to fall back to: the details
   * panel is off until asked for, at any width. */
  readonly details: boolean
  readonly compact: boolean
}

export const ui = defineStore(
  {
    theme: readPref('sc.theme', THEMES, 'system'),
    details: readPref('sc.details', DETAILS, 'closed') === 'open',
    compact: typeof window !== 'undefined' && window.innerWidth < COMPACT_MAX_PX
  } as UiState,
  (set) => ({
    setTheme(theme: ThemePref): void {
      set({ theme })
      writePref('sc.theme', theme)
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
