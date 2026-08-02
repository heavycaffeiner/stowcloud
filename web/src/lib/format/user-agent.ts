// web/src/lib/format/user-agent.ts — turns a raw `User-Agent` header into a
// short "OS · Browser" label for the active-sessions list (settings' §3.2,
// FEATURES #54). The raw string just started arriving for real (the backend
// previously never recorded it, so this column always read "unknown device")
// — but a 100+ character UA string wrapped across several lines is not
// actually more useful to a user than the placeholder was.
//
// A regex-based best-effort, not a full UA-parser dependency: this is
// display-only (nothing security- or logic-relevant reads the result), so a
// wrong guess costs nothing worse than an imprecise label, and the raw
// string is always the fallback rather than something invented.

import { t } from '../i18n'
function detectOs(ua: string): string | null {
  if (/windows/i.test(ua)) return 'Windows'
  if (/iphone|ipad|ipod/i.test(ua)) return 'iOS'
  if (/android/i.test(ua)) return 'Android'
  if (/mac os x|macintosh/i.test(ua)) return 'macOS'
  if (/cros/i.test(ua)) return 'ChromeOS'
  if (/linux/i.test(ua)) return 'Linux'
  return null
}

function detectBrowser(ua: string): string | null {
  // Order matters: Edge/OPR/Chrome UAs all also match `Safari`/`Chrome`
  // tokens they carry along for legacy sniffing, so the more specific brand
  // has to be checked first.
  if (/edg\//i.test(ua)) return 'Edge'
  if (/opr\/|opera/i.test(ua)) return 'Opera'
  if (/headlesschrome/i.test(ua)) return 'Chrome (headless)'
  if (/chrome\//i.test(ua)) return 'Chrome'
  if (/firefox\//i.test(ua)) return 'Firefox'
  if (/safari\//i.test(ua)) return 'Safari'
  return null
}

/** `raw` is what gets shown when neither half parses -- never silently
 *  dropped, since a partially-unfamiliar UA is still more informative than
 *  a placeholder. */
export function describeUserAgent(raw: string | null | undefined): string {
  if (!raw || !raw.trim()) return t('session.unknown_device')
  const os = detectOs(raw)
  const browser = detectBrowser(raw)
  if (os && browser) return `${os} · ${browser}`
  if (os) return os
  if (browser) return browser
  return raw
}
