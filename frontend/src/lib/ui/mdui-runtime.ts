import { loadLocale } from 'mdui/functions/loadLocale.js'
import { setLocale as setMduiLocale } from 'mdui/functions/setLocale.js'
import { setTheme } from 'mdui/functions/setTheme.js'
import type { Locale } from '../i18n/state'
import type { ThemePref } from '../store/ui.store'

const localeModules = {
  ko: () => import('mdui/locales/ko-kr.js')
} as const

loadLocale((locale) => {
  if (locale !== 'ko-kr') throw new Error(`Unsupported mdui locale: ${locale}`)
  return localeModules.ko()
})


import { setColorScheme } from 'mdui/functions/setColorScheme.js'

export function initMdui(): void {
  try {
    setColorScheme('#0e385e', {
      customColors: [
        { name: 'success', value: '#2e7d32' },
        { name: 'warning', value: '#ed6c02' }
      ]
    })
  } catch {
    // non-DOM environment
  }
}

export function applyMduiTheme(theme: ThemePref): void {
  setTheme(theme === 'system' ? 'auto' : theme)
}

export async function applyMduiLocale(locale: Locale): Promise<void> {
  await setMduiLocale(locale === 'ko' ? 'ko-kr' : 'en-us')
}
