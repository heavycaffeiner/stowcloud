import { createInstance } from 'i18next'
import ko from './ko.json'

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

// Korean is the fallback for every key, so it ships in the entry chunk. English
// is its own chunk, fetched before first render only when it is the choice.
export const i18n = createInstance()
void i18n.init({
  resources: { ko: { translation: ko } },
  lng: 'ko',
  supportedLngs: ['ko', 'en'],
  fallbackLng: 'ko',
  keySeparator: false,
  initAsync: false,
  interpolation: { prefix: '{', suffix: '}', escapeValue: false },
  saveMissing: false
})

async function ensureCatalogue(locale: Locale): Promise<void> {
  if (i18n.hasResourceBundle(locale, 'translation')) return
  // Dynamic on purpose: a static import would put the catalogue back into
  // the entry chunk this split keeps under its size budget.
  const { default: catalogue } = await import('./en.json')
  i18n.addResourceBundle(locale, 'translation', catalogue)
}

/** Loads the saved locale's catalogue and activates it. Awaited before the
 *  first render so no screen is drawn in the wrong language. */
export async function initLocale(): Promise<void> {
  const locale = detectLocale()
  if (locale === 'ko') return
  try {
    await ensureCatalogue(locale)
    await i18n.changeLanguage(locale)
  } catch {
    // A catalogue that fails to load leaves the Korean fallback in place.
  }
}

export function currentLocale(): Locale {
  return i18n.language === 'en' ? 'en' : 'ko'
}

export async function setLocale(locale: Locale): Promise<void> {
  if (locale !== 'ko' && locale !== 'en') throw new RangeError('Unsupported locale')
  await ensureCatalogue(locale)
  await i18n.changeLanguage(locale)
  try {
    localStorage.setItem('sc.locale', locale)
  } catch {
    // The in-memory choice remains active when persistence is unavailable.
  }
}
