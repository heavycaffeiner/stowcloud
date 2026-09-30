// Theme and layout chrome. Persisted where a choice that resets on reload
// would be a choice the user has to make again on every reload.
import { create } from 'zustand'
import { readPref, writePref } from './persist'

export type ThemePref = 'system' | 'light' | 'dark'

const THEMES: readonly ThemePref[] = ['system', 'light', 'dark']
const DETAILS = ['open', 'closed'] as const
const SIDEBAR = ['expanded', 'collapsed'] as const

export interface UiState {
  readonly theme: ThemePref
  /** Unlike navigation there is no width default to fall back to: the details
   * panel is off until asked for, at any width. */
  readonly details: boolean
  readonly sidebarCollapsed: boolean
}

export const useUiStore = create<UiState>()(() => ({
  theme: readPref('sc.theme', THEMES, 'system'),
  details: readPref('sc.details', DETAILS, 'closed') === 'open',
  sidebarCollapsed: readPref('sc.sidebar', SIDEBAR, 'expanded') === 'collapsed'
}))

const set = useUiStore.setState

export const ui = {
  setTheme(theme: ThemePref): void {
    set({ theme })
    writePref('sc.theme', theme)
  },
  setDetails(open: boolean): void {
    set({ details: open })
    writePref('sc.details', open ? 'open' : 'closed')
  },
  setSidebarCollapsed(collapsed: boolean): void {
    set({ sidebarCollapsed: collapsed })
    writePref('sc.sidebar', collapsed ? 'collapsed' : 'expanded')
  },
  toggleSidebar(): void {
    const next = !useUiStore.getState().sidebarCollapsed
    set({ sidebarCollapsed: next })
    writePref('sc.sidebar', next ? 'collapsed' : 'expanded')
  }
}
