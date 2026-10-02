// Decoding and vetting zip entry names the way the server does for a plain share.

// Matches maxArchiveNameBytes in backend/internal/preview/archive.go.
const MAX_NAME_BYTES = 4096

// Matches maxArchiveNameSampleBytes in the same file: caps what the charset
// detector sees, so an archive of a million oddly-encoded names does not
// concatenate every one of them before the first decode attempt.
export const MAX_SAMPLE_BYTES = 1 << 16

const CJK_CANDIDATES = ['euc-kr', 'shift_jis', 'gbk', 'big5'] as const
export type CjkLabel = (typeof CJK_CANDIDATES)[number]

export function isValidUtf8(bytes: Uint8Array): boolean {
  try {
    new TextDecoder('utf-8', { fatal: true }).decode(bytes)
    return true
  } catch {
    return false
  }
}

/** Scores a successful decode of `text` under `label` by how much of it
 *  looks like real text in that code page's own language: Hangul syllables
 *  for Korean, kana for Japanese, CJK ideographs for the two Chinese
 *  candidates (which share that block and so cannot be told apart this way;
 *  a decode both accept ties on candidate order in `detectCjkCharset`, gbk
 *  before big5, arbitrarily but reproducibly, the same kind of tie-break
 *  uniname.go uses). Bytes a code page still decodes to *something* despite
 *  not being written in it often land in that code page's Private Use Area
 *  or produce the replacement character; both are penalized rather than
 *  scored, so a wrong-but-decodable candidate rarely outscores the right
 *  one. */
function scriptScore(label: CjkLabel, text: string): number {
  let score = 0
  for (const ch of text) {
    const cp = ch.codePointAt(0) ?? 0
    if (cp === 0xfffd) {
      score -= 10
      continue
    }
    if (cp >= 0xe000 && cp <= 0xf8ff) {
      score -= 5
      continue
    }
    if (label === 'euc-kr' && ((cp >= 0xac00 && cp <= 0xd7a3) || (cp >= 0x1100 && cp <= 0x11ff))) score += 3
    else if (label === 'shift_jis' && cp >= 0x3040 && cp <= 0x30ff) score += 3
    else if ((label === 'gbk' || label === 'big5') && cp >= 0x4e00 && cp <= 0x9fff) score += 1
  }
  return score
}

/** Identifies which of the four legacy East Asian code pages `sample` (the
 *  concatenated raw name bytes of every entry that needs one) is written
 *  in, or `null` when none of them can even decode it: the archive falls
 *  back to CP437 in that case, same as an entry without the UTF-8 flag that
 *  the server's own detector could not place either. */
export function detectCjkCharset(sample: Uint8Array): CjkLabel | null {
  if (sample.length === 0) return null
  let best: { label: CjkLabel; score: number } | null = null
  for (const label of CJK_CANDIDATES) {
    let text: string
    try {
      text = new TextDecoder(label, { fatal: true }).decode(sample)
    } catch {
      continue
    }
    const score = scriptScore(label, text)
    if (best === null || score > best.score) best = { label, score }
  }
  return best && best.score > 0 ? best.label : null
}

/** Mirrors backend/internal/preview/archive.go's safeArchiveName: filters a
 *  decoded name for display safety. Not a path-traversal guard (nothing
 *  here ever opens the name), but a control character or an absolute path
 *  inside a name that a client might render or forward to its own
 *  extractor. */
export function isSafeArchiveName(name: string): boolean {
  if (name === '' || new TextEncoder().encode(name).length > MAX_NAME_BYTES) return false
  if (name.startsWith('/') || name.includes('\\')) return false
  if (name.length >= 2 && name[1] === ':') return false
  if (name.split('/').includes('..')) return false
  for (let i = 0; i < name.length; i++) {
    const c = name.charCodeAt(i)
    if (c < 0x20 || c === 0x7f) return false
  }
  return true
}
