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


export function applyMduiTheme(theme: ThemePref): void {
  setTheme(theme === 'system' ? 'auto' : theme)
}

export async function applyMduiLocale(locale: Locale): Promise<void> {
  await setMduiLocale(locale === 'ko' ? 'ko-kr' : 'en-us')
}
