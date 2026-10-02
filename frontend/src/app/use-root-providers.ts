import { useEffect } from 'react'
import { localeTag } from '../i18n'
import type { Locale } from '../i18n/state'

/** Keeps the document language in step with the chosen locale. */
export function useDocumentLanguage(locale: Locale): void {
  useEffect(() => {
    document.documentElement.lang = localeTag()
  }, [locale])
}
