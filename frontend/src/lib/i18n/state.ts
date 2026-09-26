import { defineStore } from '../store/create'

export type Locale = 'ko' | 'en'

function detectLocale(): Locale {
  try {
    const saved = localStorage.getItem('sc.locale')
    if (saved === 'ko' || saved === 'en') return saved
  } catch {
    // Storage can be unavailable in private contexts and tests.
  }
  return 'ko'
}

interface LocaleState {
  readonly locale: Locale
}

export const localeStore = defineStore({ locale: detectLocale() } as LocaleState, (set) => ({
  setLocale(locale: Locale): void {
    set({ locale })
    try {
      localStorage.setItem('sc.locale', locale)
    } catch {
      // The in-memory choice remains active when persistence is unavailable.
    }
  }
}))

export const setLocale = localeStore.setLocale
