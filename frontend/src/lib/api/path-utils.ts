// Small virtual-path helpers shared by the
// mock backend.: all path params are virtual paths
// (`/{label}/sub/path`); there is no real filesystem path here.

export function normalizePath(p: string): string {
  if (!p || p === '/') return '/'
  let s = p.replace(/\/+/g, '/')
  if (!s.startsWith('/')) s = `/${s}`
  if (s.length > 1 && s.endsWith('/')) s = s.slice(0, -1)
  return s
}

export function joinPath(parent: string, name: string): string {
  const p = normalizePath(parent)
  return p === '/' ? `/${name}` : `${p}/${name}`
}

export function parentOf(p: string): string {
  const n = normalizePath(p)
  if (n === '/') return '/'
  const idx = n.lastIndexOf('/')
  return idx <= 0 ? '/' : n.slice(0, idx)
}

export function baseName(p: string): string {
  const n = normalizePath(p)
  if (n === '/') return ''
  return n.slice(n.lastIndexOf('/') + 1)
}

/**
 * True when `p` is `ancestor` itself or sits anywhere under it.
 *
 * Prefix comparison alone is wrong: `/home/Doc` is not inside `/home/Documents`
 * even though the string starts with it, so the separator has to be part of
 * the match.
 */
export function isWithin(p: string, ancestor: string): boolean {
  const a = normalizePath(ancestor)
  const n = normalizePath(p)
  if (n === a) return true
  return n.startsWith(a === '/' ? '/' : `${a}/`)
}

/**
 * What is wrong with sending `sources` to the folder `dest`, or `null` when
 * nothing is. Lets the destination picker grey out its confirm button and say
 * which rule the choice broke, rather than letting the user commit and
 * reading the answer back as a failed job.
 *
 * `into_itself` is fatal for both move and copy: a folder cannot become its
 * own descendant. `same_folder` only blocks a move (it would be a no-op); a
 * copy into the source's own folder is the ordinary "duplicate" case and the
 * conflict dialog already covers it, so the caller decides that one.
 */
export type DestinationProblem = 'same_folder' | 'into_itself'

export function destinationProblem(dest: string, sources: string[]): DestinationProblem | null {
  const d = normalizePath(dest)
  let sameFolder = false
  for (const src of sources) {
    const s = normalizePath(src)
    // Covers `dest === src` as well: a folder is within itself.
    if (isWithin(d, s)) return 'into_itself'
    if (parentOf(s) === d) sameFolder = true
  }
  return sameFolder ? 'same_folder' : null
}
