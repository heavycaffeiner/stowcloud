import { useEffect } from 'react'
import { localeTag } from '../i18n'
import type { Locale } from '../i18n/state'
import { applyMduiLocale } from '../ui/mdui-runtime'

/** Keeps the document language and MDUI's translated components aligned. */
export function useMduiLocale(locale: Locale): void {
  useEffect(() => {
    document.documentElement.lang = localeTag()
    void applyMduiLocale(locale)
  }, [locale])
}
