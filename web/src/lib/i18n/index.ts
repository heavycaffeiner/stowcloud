// web/src/lib/i18n/index.ts —
// ko default + en, Intl for dates/relative-time/numbers.
//
// A call site names a key (`t('nav.files')`); the Korean and English text
// both live in catalogues, and neither language is privileged in the code.
// The alternative — using the Korean source string as its own key — was
// tried and removed: it makes `t()` a substitution table over one hard-coded
// language, so a Korean copy edit silently orphans its English, and the
// source language can never be swapped out.
//
// `web/tools/i18n-check.mjs` fails the build when a key used at a call site
// is missing from either catalogue, when a catalogue holds a key nothing
// uses, or when `{placeholder}` sets disagree between the two languages.
import en from './en.json'
import ko from './ko.json'
import { i18nState, setLocale, type Locale } from './state.svelte'

export type { Locale }
export { setLocale }

const CATALOGUE: Record<Locale, Record<string, string>> = { ko, en }

export function currentLocale(): Locale {
  return i18nState.locale
}

/**
 * `key` is a dotted catalogue key. `params` substitutes `{name}` holes, which
 * must appear in every language's text for that key.
 *
 * The fallback chain is locale → ko → the key itself. For a literal key the
 * checker refuses a build where either fallback would be reached; the last
 * hop only ever fires for a key that reaches `t()` through a variable — a
 * server-sent `reason_key` this build has never heard of, say, which means
 * the client is older than the server and rendering the key is the honest
 * answer.
 */
export function t(key: string, params?: Record<string, string | number>): string {
  let s = CATALOGUE[i18nState.locale][key] ?? ko[key as keyof typeof ko] ?? key
  if (params) {
    for (const [k, v] of Object.entries(params)) s = s.replaceAll(`{${k}}`, String(v))
  }
  return s
}

/** BCP 47 tag for the current locale — what `Intl` takes, and what the root
 *  layout writes into `<html lang>`. */
export function localeTag(): string {
  return i18nState.locale === 'ko' ? 'ko-KR' : 'en-US'
}

/** mtime_ns (nanoseconds-as-string, per DESIGN-API.md §1) → localized date/time. */
export function formatDateNs(mtimeNs: string): string {
  const ms = Number(BigInt(mtimeNs) / 1_000_000n)
  return new Intl.DateTimeFormat(localeTag(), { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(ms))
}

const RELATIVE_UNITS: [Intl.RelativeTimeFormatUnit, number][] = [
  ['year', 31_536_000],
  ['month', 2_592_000],
  ['day', 86_400],
  ['hour', 3_600],
  ['minute', 60],
  ['second', 1]
]

export function formatRelativeNs(mtimeNs: string, now = Date.now()): string {
  const ms = Number(BigInt(mtimeNs) / 1_000_000n)
  const diffSec = (ms - now) / 1000
  const rtf = new Intl.RelativeTimeFormat(localeTag(), { numeric: 'auto' })
  const abs = Math.abs(diffSec)
  for (const [unit, secs] of RELATIVE_UNITS) {
    if (abs >= secs || unit === 'second') return rtf.format(Math.round(diffSec / secs), unit)
  }
  return rtf.format(0, 'second')
}

export function formatNumber(n: number): string {
  return new Intl.NumberFormat(localeTag()).format(n)
}

/**
 * A rough duration in the unit a person would say out loud — "3 minutes",
 * "2.5 hours", "45초" — for estimates, where a stopwatch reading like
 * "1h 12m 04s" implies a precision the number does not have.
 *
 * `Intl` supplies both the unit word and its plural, so this needs no
 * catalogue entry per unit and reads correctly in a language that has no
 * plural forms at all.
 */
export function formatDuration(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 0) return '—'
  const [unit, value] =
    seconds < 60
      ? (['second', Math.max(1, Math.round(seconds))] as const)
      : seconds < 3600
        ? (['minute', Math.round(seconds / 60)] as const)
        : (['hour', Math.round((seconds / 3600) * 10) / 10] as const)
  return new Intl.NumberFormat(localeTag(), {
    style: 'unit',
    unit,
    unitDisplay: 'long',
    maximumFractionDigits: 1
  }).format(value)
}
