import { createInstance } from 'i18next'
import en from './en.json'
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

export const i18n = createInstance()
void i18n.init({
  resources: { ko: { translation: ko }, en: { translation: en } },
  lng: detectLocale(),
  supportedLngs: ['ko', 'en'],
  fallbackLng: 'ko',
  keySeparator: false,
  initAsync: false,
  interpolation: { prefix: '{', suffix: '}', escapeValue: false },
  saveMissing: false
})

export function currentLocale(): Locale {
  return i18n.language === 'en' ? 'en' : 'ko'
}

export function setLocale(locale: Locale): void {
  if (locale !== 'ko' && locale !== 'en') throw new RangeError('Unsupported locale')
  void i18n.changeLanguage(locale)
  try {
    localStorage.setItem('sc.locale', locale)
  } catch {
    // The in-memory choice remains active when persistence is unavailable.
  }
}
