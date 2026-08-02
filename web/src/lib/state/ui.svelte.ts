// web/src/lib/state/ui.svelte.ts — theme + layout chrome preferences.
export type ThemePref = 'system' | 'light' | 'dark'

function detectTheme(): ThemePref {
  try {
    const saved = localStorage.getItem('sc.theme')
    if (saved === 'light' || saved === 'dark' || saved === 'system') return saved
  } catch {
    /* ignore */
  }
  return 'system'
}

/**
 * `null` means the user has never opened or closed the root drawer, so the
 * width-based default in `(app)/+layout.svelte` still applies. Without this
 * the default was re-applied on every load — closing the drawer lasted until
 * the next refresh, which is not what closing something means.
 */
function detectDrawer(): boolean | null {
  try {
    const saved = localStorage.getItem('sc.drawer')
    if (saved === 'open') return true
    if (saved === 'closed') return false
  } catch {
    /* ignore */
  }
  return null
}

export const uiState = $state({
  theme: detectTheme() as ThemePref,
  drawer: detectDrawer() as boolean | null,
  /** MD3 window class breakpoint (DESIGN-FRONTEND.md §3): rail vs bar+drawer. */
  compact: typeof window !== 'undefined' ? window.innerWidth < 905 : false
})

export function setTheme(t: ThemePref): void {
  uiState.theme = t
  try {
    localStorage.setItem('sc.theme', t)
  } catch {
    /* ignore */
  }
  applyTheme()
}

export function setDrawer(open: boolean): void {
  uiState.drawer = open
  try {
    localStorage.setItem('sc.drawer', open ? 'open' : 'closed')
  } catch {
    /* ignore */
  }
}

export function applyTheme(): void {
  const root = document.documentElement
  if (uiState.theme === 'system') root.removeAttribute('data-theme')
  else root.setAttribute('data-theme', uiState.theme)
}

export function watchViewport(): () => void {
  function onResize() {
    uiState.compact = window.innerWidth < 905
  }
  window.addEventListener('resize', onResize)
  onResize()
  return () => window.removeEventListener('resize', onResize)
}
