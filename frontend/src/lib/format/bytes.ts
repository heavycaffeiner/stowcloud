// One unit vocabulary for the whole product:
// KB/MB/GB, each 1024 of the one below.
//
// That is the consumer convention (Windows Explorer, macOS pre-10.6, every
// storage device's own settings screen), not the SI one, and it is the
// definition every size *input* in this app uses too: "5 MB" typed into the
// chunk-size field is exactly the server's 5 * 1024 * 1024 floor, and a file
// this function renders as "5 MB" is the same size as that field's value.
// Showing MiB here while the fields said MB would make the two disagree by
// 4.9% for no reader's benefit; showing true SI MB would make every
// server-side constant a non-round number on screen (the 5 MiB floor reads
// as "5.24 MB").
//
// `Intl.NumberFormat` supplies locale-aware digit grouping/decimal marks; it
// has no notion of this unit scheme, so the suffix is chosen by hand and only
// the numeric magnitude goes through Intl.

/** Bytes in one "MB" as this app uses the word. The single definition:
 *  every megabyte-denominated setting converts through this constant. */
export const BYTES_PER_MB = 1024 * 1024

const UNITS = ['B', 'KB', 'MB', 'GB', 'TB', 'PB', 'EB'] as const
const BASE = 1024

export interface FormatBytesOptions {
  locale?: string
  maximumFractionDigits?: number
}

export function formatBytes(bytes: number, opts: FormatBytesOptions = {}): string {
  const { locale = 'en', maximumFractionDigits = 1 } = opts
  if (!Number.isFinite(bytes)) return '-'
  const sign = bytes < 0 ? '-' : ''
  const abs = Math.abs(bytes)

  if (abs < BASE) {
    const nf = new Intl.NumberFormat(locale, { maximumFractionDigits: 0 })
    return `${sign}${nf.format(abs)} ${UNITS[0]}`
  }

  const exp = Math.min(Math.floor(Math.log(abs) / Math.log(BASE)), UNITS.length - 1)
  const value = abs / BASE ** exp
  const nf = new Intl.NumberFormat(locale, { maximumFractionDigits })
  return `${sign}${nf.format(value)} ${UNITS[exp]}`
}

/** Bytes → whole MB, for prefilling a megabyte-denominated input. */
export function bytesToMb(bytes: number): number {
  return Math.round(bytes / BYTES_PER_MB)
}

/** Bytes-per-second → human rate, e.g. "4.2 MB/s". */
export function formatRate(bytesPerSecond: number, opts: FormatBytesOptions = {}): string {
  if (!Number.isFinite(bytesPerSecond) || bytesPerSecond <= 0) return '-'
  return `${formatBytes(bytesPerSecond, opts)}/s`
}

/** Seconds remaining → "3m 12s" / "12s" / "-" style ETA string. */
export function formatEta(seconds: number, locale = 'en'): string {
  if (!Number.isFinite(seconds) || seconds < 0) return '-'
  if (seconds < 1) return '<1s'
  const total = Math.round(seconds)
  const h = Math.floor(total / 3600)
  const m = Math.floor((total % 3600) / 60)
  const s = total % 60
  const nf = new Intl.NumberFormat(locale)
  const parts: string[] = []
  if (h > 0) parts.push(`${nf.format(h)}h`)
  if (h > 0 || m > 0) parts.push(`${nf.format(m)}m`)
  parts.push(`${nf.format(s)}s`)
  return parts.join(' ')
}
