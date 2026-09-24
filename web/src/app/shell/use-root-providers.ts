import { useEffect } from 'react'
import { localeTag } from '../../lib/i18n'
import type { Locale } from '../../lib/i18n/state'
import { applyMduiLocale, applyMduiTheme, initMdui } from '../../lib/ui/mdui-runtime'
import type { ThemePref } from '../../lib/store/ui.store'

/** Initializes MDUI's color tokens once after the document becomes available. */
export function useMduiBootstrap(): void {
  useEffect(() => {
    initMdui()
  }, [])
}

/** Applies the selected theme to MDUI whenever the preference changes. */
export function useMduiTheme(theme: ThemePref): void {
  useEffect(() => {
    applyMduiTheme(theme)
  }, [theme])
}

/** Keeps the document language and MDUI's translated components aligned. */
export function useMduiLocale(locale: Locale): void {
  useEffect(() => {
    document.documentElement.lang = localeTag()
    void applyMduiLocale(locale)
  }, [locale])
}
