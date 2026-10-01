// The search stream: hits arrive as server-sent events until the server is
// done or reports that its bounded walk stopped early.
import { apiUrl } from '../../api/fetcher'
import { normalizePath } from '../../lib/path-utils'
import type { Entry } from '../files/api'

export interface SearchHit {
  path: string
  /** Only what the hit carries: no etag, so nothing here may back a
   *  conditional write. */
  entry: Pick<Entry, 'name' | 'path' | 'kind' | 'size' | 'mtime_ns'>
}

/** An absent `kind` or an empty `exts` narrows nothing. `scope` ranks that
 *  subtree up; it does not confine the search to it. */
export interface SearchRequest {
  query: string
  kind?: 'file' | 'dir'
  exts?: readonly string[]
  scope?: string
}

export interface SearchDone {
  /** How many hits the server sent; fewer arriving means frames were lost. */
  count: number
  tier?: string
  /** The server stopped before checking every accessible folder. */
  truncated?: boolean
  elapsedMs?: number
  /** `busy`, `search_failed`, or `network` when the stream itself broke. */
  error?: string
}

/** How far the walk has got. Never an estimate of what is left. */
export interface SearchProgress {
  dirs: number
  files: number
  found: number
}

function parsed(data: unknown): Record<string, unknown> | null {
  if (typeof data !== 'string') return null
  try {
    const value: unknown = JSON.parse(data)
    return value !== null && typeof value === 'object' ? (value as Record<string, unknown>) : null
  } catch {
    return null
  }
}

/** The frame is untrusted; one that does not hold a hit is dropped. `size` is
 *  a decimal string because a size past 2^53 loses digits as a JSON number. */
function hitFromFrame(raw: Record<string, unknown> | null): SearchHit | null {
  if (raw === null || typeof raw.path !== 'string' || typeof raw.name !== 'string') return null
  const size = typeof raw.size === 'string' && /^(?:0|[1-9]\d*)$/.test(raw.size) ? Number(raw.size) : 0
  const path = normalizePath(raw.path)
  return {
    path,
    entry: {
      name: raw.name,
      path,
      kind: raw.is_dir === true ? 'dir' : 'file',
      size: Number.isSafeInteger(size) ? size : 0,
      mtime_ns: typeof raw.mtime_ns === 'string' ? raw.mtime_ns : '0'
    }
  }
}

function numberOr(value: unknown, fallback: number): number {
  return typeof value === 'number' ? value : fallback
}

/** Starts one search and returns the function that stops it. `metadata=1`
 *  asks for the size and date the result list shows. */
export function searchStream(
  req: SearchRequest,
  onHit: (hit: SearchHit) => void,
  onDone: (done: SearchDone) => void,
  onProgress?: (p: SearchProgress) => void
): () => void {
  const ext = req.exts && req.exts.length > 0 ? req.exts.join(',') : undefined
  const url = apiUrl('/search/stream', { q: req.query, kind: req.kind, ext, path: req.scope, metadata: '1' })
  const es = new EventSource(url, { withCredentials: true })
  let count = 0

  es.addEventListener('hit', (ev) => {
    count++
    const hit = hitFromFrame(parsed(ev.data))
    if (hit) onHit(hit)
  })
  es.addEventListener('progress', (ev) => {
    const raw = parsed(ev.data)
    if (!onProgress || raw === null) return
    onProgress({ dirs: numberOr(raw.dirs, 0), files: numberOr(raw.files, 0), found: numberOr(raw.found, count) })
  })
  es.addEventListener('done', (ev) => {
    // A `done` with no readable payload still ends the search.
    const raw = parsed(ev.data) ?? {}
    onDone({
      count: numberOr(raw.count, count),
      tier: typeof raw.tier === 'string' ? raw.tier : undefined,
      truncated: raw.truncated === true,
      elapsedMs: typeof raw.elapsed_ms === 'number' ? raw.elapsed_ms : undefined,
      error: typeof raw.error === 'string' ? raw.error : undefined
    })
    es.close()
  })
  es.onerror = () => {
    es.close()
    // What arrived is a prefix, and the list must not present it as the whole answer.
    onDone({ count, error: 'network', truncated: false })
  }
  return () => es.close()
}
