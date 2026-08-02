// web/src/lib/i18n/state.svelte.ts — current-locale rune. Kept in its own
// `.svelte.ts` file so plain `.ts` consumers (index.ts) can re-export from it.
export type Locale = 'ko' | 'en'

function detect(): Locale {
  try {
    const saved = localStorage.getItem('sc.locale')
    if (saved === 'ko' || saved === 'en') return saved
  } catch {
    /* localStorage unavailable (SSR/tests) */
  }
  return 'ko'
}

export const i18nState = $state({ locale: detect() })

export function setLocale(l: Locale): void {
  i18nState.locale = l
  try {
    localStorage.setItem('sc.locale', l)
  } catch {
    /* ignore */
  }
}
