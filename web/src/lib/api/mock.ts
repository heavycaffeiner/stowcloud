// web/src/lib/api/mock.ts — in-memory mock of, swapped in via
// VITE_API_MOCK=1 (see client.ts). Implements listing sessions + cursor
// pagination exactly like the real server so FileTable/BrowseState code
// never has to know which backend it's talking to.
import {
  BENCH_COUNT,
  BENCH_DIR,
  STATIC_SEED,
  benchEntryAt,
  compareEntries
} from './mock-seed'
import { baseName, joinPath, normalizePath, parentOf } from './path-utils'
import { CHUNK_SIZE_MIN } from '../upload/chunk-planner'
import {
  ApiError,
  type ActiveSession,
  type AdminGrant,
  type AdminGroup,
  type AdminOidcUnlinkResult,
  type AdminShare,
  type AdminUser,
  type AdminUserOidc,
  type AppPasswordInfo,
  type ApplyOutcome,
  type ArchiveSettingsReq,
  type AuditPage,
  type AuditQuery,
  type AuditRow,
  type BatchItemResult,
  type BatchResult,
  type CreateGrantReq,
  type CreateGroupReq,
  type CreateShareReq,
  type DbSettingsReq,
  type Entry,
  type HomesSettingsReq,
  type IndexEstimate,
  type IndexSettings,
  type JobKindWire,
  type JobListResponse,
  type JobState,
  type JobStatus,
  type LinkDisposition,
  type LinkResponse,
  type ListResponse,
  type LoginResult,
  type MovePreflight,
  type MoveReq,
  type NetworkSettingsReq,
  type OidcSettingsReq,
  type Order,
  type PathsSettingsReq,
  type SearchSettingsReq,
  type SessionInfo,
  type SettingsField,
  type SettingsSnapshot,
  type ShareLinkCreateReq,
  type ShareLinkInfo,
  type ShareLinkPatchReq,
  type SmbSettingsReq,
  type SortKey,
  type StorageReport,
  type SymlinkPolicyReq,
  type TrashEntry,
  type UpdateGrantReq,
  type UpdateGroupReq,
  type UpdateShareReq,
  type UploadSettingsReq,
  type UploadSettingsResp,
  type WatchSettingsReq
} from './types'

function delay(ms = 30, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) {
      reject(new DOMException('Aborted', 'AbortError'))
      return
    }
    const t = setTimeout(resolve, ms)
    signal?.addEventListener(
      'abort',
      () => {
        clearTimeout(t)
        reject(new DOMException('Aborted', 'AbortError'))
      },
      { once: true }
    )
  })
}

function randomId(prefix: string): string {
  return `${prefix}-${Math.random().toString(36).slice(2, 10)}`
}

/** Numeric fileids for entries created at runtime (`mkdir`/`writeFile`) —
 *  `Entry.id` is `number | undefined` (mirrors the real `fid`, see that
 *  field's doc comment in `types.ts`), so the old `randomId('id')` string
 *  no longer type-checks. Starts well above `mock-seed.ts`'s own ranges
 *  (static seed: low fixed numbers; `/bench`: index-based) so nothing
 *  collides. Unlike the real backend, every mock entry gets one — the mock
 *  represents the intended contract ('s whole
 *  content-origin design assumes a visible entry has a usable fid), not the
 *  real server's current allocation gap, so download is fully exercisable
 *  against `VITE_API_MOCK=1` even though it frequently isn't against the
 *  live server today. */
let nextMockFileId = 1_000_000
function newMockFileId(): number {
  return nextMockFileId++
}

// ── mutable mock filesystem state (overlay atop the deterministic seed) ──
const overlay = new Map<string, Map<string, Entry>>() // dir path -> name -> entry
const tombstones = new Map<string, Set<string>>() // dir path -> deleted names
const dirVersion = new Map<string, number>()
/** Text content for the `/edit` route's mock backend. The static seed has no
 *  real bytes behind it (`benchEntryAt`/`fileEntry` only produce metadata),
 *  so a file that hasn't been written yet gets a deterministic placeholder
 *  instead of a lookup miss. */
const fileContents = new Map<string, string>()

function bumpVersion(dirPath: string): void {
  const n = normalizePath(dirPath)
  dirVersion.set(n, (dirVersion.get(n) ?? 0) + 1)
}

function isTombstoned(dirPath: string, name: string): boolean {
  return tombstones.get(normalizePath(dirPath))?.has(name) ?? false
}

function addOverlayEntry(dirPath: string, entry: Entry): void {
  const n = normalizePath(dirPath)
  if (!overlay.has(n)) overlay.set(n, new Map())
  overlay.get(n)!.set(entry.name, entry)
  const dead = tombstones.get(n)
  dead?.delete(entry.name)
  bumpVersion(n)
}

function removeEntry(dirPath: string, name: string): void {
  const n = normalizePath(dirPath)
  overlay.get(n)?.delete(name)
  if (!tombstones.has(n)) tombstones.set(n, new Set())
  tombstones.get(n)!.add(name)
  bumpVersion(n)
}

function baseEntriesFor(dirPath: string): Entry[] {
  if (dirPath === BENCH_DIR) {
    const out: Entry[] = new Array(BENCH_COUNT)
    for (let i = 0; i < BENCH_COUNT; i++) out[i] = benchEntryAt(i)
    return out
  }
  const seed = STATIC_SEED.find((d) => d.path === dirPath)
  return seed ? seed.entries.slice() : []
}

function resolveDirEntries(dirPath: string): Entry[] {
  const n = normalizePath(dirPath)
  const base = baseEntriesFor(n)
  const dead = tombstones.get(n)
  const filtered = dead && dead.size ? base.filter((e) => !dead.has(e.name)) : base
  const additions = overlay.get(n)
  if (!additions || additions.size === 0) return filtered
  const byName = new Map(filtered.map((e) => [e.name, e]))
  for (const e of additions.values()) byName.set(e.name, e)
  return [...byName.values()]
}

function entryAt(path: string): Entry | null {
  const n = normalizePath(path)
  if (n === '/') return null
  const parent = parentOf(n)
  const name = baseName(n)
  return resolveDirEntries(parent).find((e) => e.name === name) ?? null
}

function dirEtagFor(dirPath: string): string {
  const n = normalizePath(dirPath)
  const count = n === BENCH_DIR ? BENCH_COUNT : baseEntriesFor(n).length
  const v = dirVersion.get(n) ?? 0
  const deadCount = tombstones.get(n)?.size ?? 0
  const addCount = overlay.get(n)?.size ?? 0
  return `v${v}-${count}-${deadCount}-${addCount}`
}

// ── listing sessions ──
interface ListingSession {
  id: string
  path: string
  sort: SortKey
  order: Order
  entries: Entry[]
  dirEtag: string
  createdAt: number
}

const LISTINGS = new Map<string, ListingSession>()
const MAX_LISTINGS = 6

function evictOldListings(): void {
  if (LISTINGS.size <= MAX_LISTINGS) return
  const oldest = [...LISTINGS.values()].sort((a, b) => a.createdAt - b.createdAt)
  for (let i = 0; i < LISTINGS.size - MAX_LISTINGS; i++) LISTINGS.delete(oldest[i].id)
}

function createListingSession(path: string, sort: SortKey, order: Order): ListingSession {
  const n = normalizePath(path)
  const entries = resolveDirEntries(n)
  entries.sort((a, b) => compareEntries(a, b, sort, order))
  const session: ListingSession = {
    id: randomId('L'),
    path: n,
    sort,
    order,
    entries,
    dirEtag: dirEtagFor(n),
    createdAt: Date.now()
  }
  LISTINGS.set(session.id, session)
  evictOldListings()
  return session
}

function encodeCursor(offset: number, total: number): string | null {
  if (offset >= total) return null
  return btoa(JSON.stringify({ i: offset }))
}

function decodeCursor(cursor: string): number {
  try {
    const parsed = JSON.parse(atob(cursor))
    return typeof parsed.i === 'number' ? parsed.i : 0
  } catch {
    return 0
  }
}

export interface ListOpts {
  sort?: SortKey
  order?: Order
  listing?: string
  cursor?: string
  /**
   * Random-access window start within an existing listing session — the
   * server-side session already holds the fully sorted name vector,
   * so "start at index N" is just a slice, not a
   * sequential cursor walk. Used for scroll-driven windowed fetches instead
   * of chaining `cursor` one page at a time. Takes precedence over `cursor`
   * when both are present.
   */
  offset?: number
  limit?: number
  /** Lets the caller cancel a windowed fetch for a range it has scrolled past. */
  signal?: AbortSignal
}

async function list(path: string, opts: ListOpts): Promise<ListResponse> {
  await delay(30, opts.signal)
  const limit = opts.limit ?? 200

  if (opts.listing) {
    const session = LISTINGS.get(opts.listing)
    if (!session) {
      throw new ApiError(409, { code: 'fs.listing_expired', message: 'listing session expired' })
    }
    const currentEtag = dirEtagFor(session.path)
    if (currentEtag !== session.dirEtag) {
      const fresh = createListingSession(session.path, session.sort, session.order)
      const freshOffset = opts.offset ?? 0
      const page = fresh.entries.slice(freshOffset, freshOffset + limit)
      return {
        listing: fresh.id,
        total: fresh.entries.length,
        cursor: encodeCursor(freshOffset + page.length, fresh.entries.length),
        entries: page,
        dir_etag: fresh.dirEtag,
        stale: true
      }
    }
    if (opts.offset !== undefined) {
      // Random-access window:'s listing session already
      // holds the sorted vector, so any offset is a plain slice.
      const offset = Math.max(0, opts.offset)
      const page = session.entries.slice(offset, offset + limit)
      return {
        listing: session.id,
        total: session.entries.length,
        cursor: encodeCursor(offset + page.length, session.entries.length),
        entries: page,
        dir_etag: session.dirEtag
      }
    }
    if (!opts.cursor) {
      throw new ApiError(409, { code: 'fs.listing_expired', message: 'listing session expired' })
    }
    const offset = decodeCursor(opts.cursor)
    const page = session.entries.slice(offset, offset + limit)
    return {
      listing: session.id,
      total: session.entries.length,
      cursor: encodeCursor(offset + page.length, session.entries.length),
      entries: page,
      dir_etag: session.dirEtag
    }
  }

  if (opts.cursor) {
    // cursor without a listing id: session presumed expired.
    throw new ApiError(409, { code: 'fs.listing_expired', message: 'listing session expired' })
  }

  const sort = opts.sort ?? 'name'
  const order = opts.order ?? 'asc'
  const session = createListingSession(path, sort, order)
  const startOffset = opts.offset ?? 0
  const page = session.entries.slice(startOffset, startOffset + limit)
  return {
    listing: session.id,
    total: session.entries.length,
    cursor: encodeCursor(startOffset + page.length, session.entries.length),
    entries: page,
    dir_etag: session.dirEtag
  }
}

async function stat(path: string): Promise<Entry> {
  await delay(10)
  const e = entryAt(path)
  if (!e) throw new ApiError(404, { code: 'fs.not_found', message: 'not found', detail: { path } })
  return e
}

function defaultPerms(): Entry['perms'] {
  return { read: true, write: true, create: true, delete: true, rename: true, move: true, share: true, download: true }
}

async function mkdir(path: string): Promise<Entry> {
  await delay()
  const n = normalizePath(path)
  const parent = parentOf(n)
  const name = baseName(n)
  if (resolveDirEntries(parent).some((e) => e.name === name)) {
    throw new ApiError(409, { code: 'fs.conflict', message: 'destination already exists', detail: { path: n } })
  }
  const entry: Entry = {
    name,
    kind: 'dir',
    size: 0,
    mtime_ns: (BigInt(Date.now()) * 1_000_000n).toString(),
    etag: randomId('e'),
    perms: defaultPerms(),
    id: newMockFileId()
  }
  addOverlayEntry(parent, entry)
  return entry
}

async function rename(path: string, newName: string): Promise<Entry> {
  await delay()
  const n = normalizePath(path)
  const parent = parentOf(n)
  const oldName = baseName(n)
  const existing = resolveDirEntries(parent).find((e) => e.name === oldName)
  if (!existing) throw new ApiError(404, { code: 'fs.not_found', message: 'not found', detail: { path: n } })
  if (resolveDirEntries(parent).some((e) => e.name === newName)) {
    throw new ApiError(409, { code: 'fs.conflict', message: 'destination already exists', detail: { path: joinPath(parent, newName) } })
  }
  const renamed: Entry = { ...existing, name: newName, etag: randomId('e') }
  removeEntry(parent, oldName)
  addOverlayEntry(parent, renamed)
  return renamed
}

/** Backs both `copy()` and `move()` — the two differ only in whether the
 *  source survives, so the conflict, `if_match` and rename-suffix behaviour
 *  they must agree on lives here once. */
async function transfer(req: MoveReq, keepSource: boolean): Promise<{ job: string }> {
  await delay()
  const results: BatchResult['results'] = []
  for (const p of req.paths) {
    try {
      const n = normalizePath(p)
      const parent = parentOf(n)
      const name = baseName(n)
      const entry = resolveDirEntries(parent).find((e) => e.name === name)
      if (!entry) throw new ApiError(404, { code: 'fs.not_found', message: 'not found', detail: { path: n } })

      const destDir = normalizePath(req.dest)
      const destExists = resolveDirEntries(destDir).some((e) => e.name === name)
      if (destExists && req.on_conflict === 'Fail') {
        throw new ApiError(409, { code: 'fs.conflict', message: 'destination already exists', detail: { path: joinPath(destDir, name) } })
      }
      if (destExists && req.on_conflict === 'Skip') {
        results.push({ path: n, ok: true })
        continue
      }
      if (destExists && req.on_conflict === 'Overwrite') {
        const wantEtag = req.if_match?.[n]
        const currentEtag = resolveDirEntries(destDir).find((e) => e.name === name)?.etag
        if (wantEtag && wantEtag !== currentEtag) {
          throw new ApiError(412, {
            code: 'fs.precondition',
            message: 'If-Match precondition failed',
            detail: { current_etag: currentEtag }
          })
        }
      }

      let finalName = name
      if (destExists && req.on_conflict === 'Rename') {
        let i = 1
        while (resolveDirEntries(destDir).some((e) => e.name === finalName)) {
          finalName = `${name} (${i++})`
        }
      }

      addOverlayEntry(destDir, { ...entry, name: finalName, etag: randomId('e') })
      if (!keepSource) removeEntry(parent, name)
      results.push({ path: n, ok: true })
    } catch (err) {
      const e = err instanceof ApiError ? err : new ApiError(500, { code: 'internal', message: 'internal error' })
      results.push({ path: p, ok: false, error: { code: e.code, message: e.message, detail: e.detail } })
    }
  }
  return { job: makeMockJob(keepSource ? 'copy' : 'move', req.paths.length, results) }
}

async function copy(req: MoveReq): Promise<{ job: string }> {
  return transfer(req, true)
}

async function move(req: MoveReq): Promise<{ job: string }> {
  return transfer(req, false)
}

/** The mock tree is one device, so a move here is always a rename and never
 *  the copy-then-delete fallback the real server warns about. */
async function movePreflight(_req: MoveReq): Promise<MovePreflight> {
  await delay(10)
  return { will_copy: false, total_bytes: 0, reason: '' }
}

async function del(paths: string[], permanent = false): Promise<{ job: string }> {
  await delay()
  const results: BatchResult['results'] = []
  for (const p of paths) {
    const n = normalizePath(p)
    const parent = parentOf(n)
    const name = baseName(n)
    const entry = resolveDirEntries(parent).find((e) => e.name === name)
    if (!entry) {
      results.push({ path: n, ok: false, error: { code: 'fs.not_found', message: 'not found' } })
      continue
    }
    removeEntry(parent, name)
    // `permanent: false` (the default — `DeleteDialog.svelte`'s "this moves
    // it to trash") lands in the mock
    // trash store instead of vanishing outright, so `/trash` has something
    // real to list/restore in `VITE_API_MOCK=1` mode too.
    if (!permanent) trashInsert(entry, parent)
    results.push({ path: n, ok: true })
  }
  return { job: makeMockJob('delete', paths.length, results) }
}

// ── long-running jobs — the real server always answers
// `202 { job }`, never a synchronous result, so this mock must too for UI
// code-path parity. Unlike the real backend, this mock's file operations
// above already ran to completion synchronously by the time `{ job }` is
// handed back (there is no mock filesystem large enough for a fake delay to
// mean anything) — `makeMockJob` just records the already-known outcome
// under a fresh id so `jobStatus`/`pollJob` see a normal `done` job on the
// very first poll, same shape a real terminal job has. ──

interface MockJobRow {
  kind: JobKindWire
  state: JobState
  done: number
  total: number
  results: BatchItemResult[]
  blob?: Blob
}

const mockJobs = new Map<string, MockJobRow>()

function makeMockJob(kind: JobKindWire, total: number, results: BatchItemResult[]): string {
  const id = randomId('job')
  // A single failed item ends the whole job in `error` on the real server
  // (`spawn_batch_job`'s `all_ok` check, `crates/sc-http/src/routes.rs`). This
  // used to be a hardcoded `done`, which made the mock report a rejected
  // conflict as a success: the UI's conflict dialog hangs off `pollJob`
  // rejecting, so against the mock it could never open and the whole
  // conflict path looked implemented and untested at the same time.
  const state: JobState = results.every((r) => r.ok) ? 'done' : 'error'
  mockJobs.set(id, { kind, state, done: total, total, results })
  return id
}

async function jobList(): Promise<JobListResponse> {
  await delay(10)
  // Every mock job is already `done` by the time it exists (see
  // `makeMockJob`'s comment) — `list_open` on the real server only ever
  // returns `running`/`interrupted` jobs, so there is never anything here
  // to re-attach to. An empty list is the honest mock answer, not a stub.
  return { jobs: [] }
}

async function jobStatus(id: string): Promise<JobStatus> {
  await delay(10)
  if (id === MOCK_INDEX_BUILD_JOB && mockIndexBuildStartedAt > 0) {
    const elapsed = Date.now() - mockIndexBuildStartedAt
    const done = Math.min(MOCK_INDEX_BUILD_SHARES, Math.floor(elapsed / MOCK_INDEX_BUILD_MS_PER_SHARE))
    const finished = done >= MOCK_INDEX_BUILD_SHARES
    return {
      id,
      kind: 'index_build',
      state: finished ? 'done' : 'running',
      done,
      total: MOCK_INDEX_BUILD_SHARES,
      current: finished ? null : 'home',
      errors: [],
      results: [],
      attempting: [],
      pending: [],
      download: false
    }
  }
  const job = mockJobs.get(id)
  if (job) {
    return {
      id,
      kind: job.kind,
      state: job.state,
      done: job.done,
      total: job.total,
      current: null,
      errors: job.results.filter((r) => !r.ok).map((r) => r.error?.message ?? 'internal error'),
      results: job.results,
      attempting: [],
      pending: [],
      download: job.blob !== undefined
    }
  }
  throw new ApiError(404, { code: 'fs.not_found', message: 'not found', detail: { id } })
}

async function jobCancel(id: string): Promise<void> {
  await delay(10)
  // Every mock job is already `done` by the time its id exists (see
  // `makeMockJob`) — there is never anything still running to cancel.
  throw new ApiError(404, { code: 'fs.not_found', message: 'not found', detail: { id } })
}

async function jobDownload(id: string): Promise<Blob> {
  await delay(10)
  const job = mockJobs.get(id)
  if (job?.blob) return job.blob
  throw new ApiError(404, { code: 'fs.not_found', message: 'not found', detail: { id } })
}

// ── trash (mirrors `crates/sc-core/src/trash.rs` closely enough to drive the
// UI in dev mode: an id is opaque, restoring back to an occupied name
// conflicts, purging is permanent) ──

interface MockTrashRow {
  id: string
  entry: Entry
  originalDir: string
  deletedNs: string
}

const mockTrash = new Map<string, MockTrashRow>()

function trashInsert(entry: Entry, originalDir: string): void {
  const id = randomId('trash')
  mockTrash.set(id, { id, entry, originalDir, deletedNs: String(BigInt(Date.now()) * 1_000_000n) })
}

async function trashList(): Promise<TrashEntry[]> {
  await delay(20)
  return [...mockTrash.values()]
    .sort((a, b) => Number(BigInt(b.deletedNs) - BigInt(a.deletedNs)))
    .map((row) => ({ id: row.id, name: row.entry.name, size: row.entry.size, deleted_mtime_ns: row.deletedNs }))
}

async function trashRestore(ids: string[]): Promise<BatchResult> {
  await delay(40)
  const results: BatchResult['results'] = []
  for (const id of ids) {
    const row = mockTrash.get(id)
    if (!row) {
      results.push({ path: id, ok: false, error: { code: 'fs.not_found', message: 'not found' } })
      continue
    }
    if (resolveDirEntries(row.originalDir).some((e) => e.name === row.entry.name)) {
      results.push({ path: id, ok: false, error: { code: 'fs.conflict', message: 'destination already exists' } })
      continue
    }
    addOverlayEntry(row.originalDir, row.entry)
    mockTrash.delete(id)
    results.push({ path: id, ok: true })
  }
  return { results }
}

async function trashPurge(ids: string[]): Promise<BatchResult> {
  await delay(40)
  const results: BatchResult['results'] = []
  for (const id of ids) {
    if (!mockTrash.has(id)) {
      results.push({ path: id, ok: false, error: { code: 'fs.not_found', message: 'not found' } })
      continue
    }
    mockTrash.delete(id)
    results.push({ path: id, ok: true })
  }
  return { results }
}

// ── content links & archive download (§8) ──
// Every mock entry carries a numeric `id` (see `newMockFileId`'s comment),
// so unlike the real backend today, `link()` never has to refuse for lack
// of a fid — it always resolves to *some* entry (falling back to a generic
// placeholder name if the id doesn't match anything still in the tree,
// which is enough for the UI's own "does this resolve" path to exercise).

function findEntryById(id: number): { entry: Entry; dirPath: string } | null {
  for (const dir of STATIC_SEED) {
    const hit = resolveDirEntries(dir.path).find((e) => e.id === id)
    if (hit) return { entry: hit, dirPath: dir.path }
  }
  for (const [dirPath, byName] of overlay) {
    for (const e of byName.values()) {
      if (e.id === id) return { entry: e, dirPath }
    }
  }
  return null
}

async function link(fid: number, _disposition: LinkDisposition = 'attachment', _dim?: [number, number]): Promise<LinkResponse> {
  await delay(40)
  const hit = findEntryById(fid)
  if (!hit) throw new ApiError(404, { code: 'fs.not_found', message: 'not found' })
  // A real, fetchable same-origin URL (not a `#`-fragment placeholder) so a
  // component under test can actually follow it — a tiny same-origin data
  // URL would be more "correct" but `blob:`/`data:` URLs can't carry a
  // filename for `Content-Disposition`, and this only needs to prove the
  // wiring, not simulate the signed-URL crypto.
  return { url: `${typeof location !== 'undefined' ? location.origin : ''}/mock-download/${encodeURIComponent(hit.entry.name)}` }
}

/** Mock counterpart of `http.ts`'s `archive` — always answers `{ job }` like
 *  the real server, with a tiny valid (if fake) zip-shaped byte blob already
 *  sitting behind it so `jobDownload` has something plausible to hand back
 *  once the job (immediately, in this mock) reports `download: true`. */
async function archive(paths: string[]): Promise<{ job: string }> {
  await delay(120)
  if (paths.length === 0) throw new ApiError(422, { code: 'fs.invalid_name', message: 'paths must not be empty' })
  const body = `mock zip archive of:\n${paths.join('\n')}\n`
  const id = makeMockJob(
    'archive',
    paths.length,
    paths.map((p) => ({ path: p, ok: true }))
  )
  mockJobs.get(id)!.blob = new Blob([body], { type: 'application/zip' })
  return { job: id }
}

// ── text editor (`/edit/[...path]`) ──

async function readFile(path: string): Promise<{ content: string }> {
  await delay(20)
  const n = normalizePath(path)
  const e = entryAt(n)
  if (!e) throw new ApiError(404, { code: 'fs.not_found', message: 'not found', detail: { path: n } })
  if (e.kind === 'dir') {
    throw new ApiError(409, { code: 'fs.conflict', message: 'destination already exists', detail: { path: n } })
  }
  if (fileContents.has(n)) return { content: fileContents.get(n)! }
  return { content: `${e.name}\n\n(목업 백엔드: 실제 파일 내용이 없어 예시 텍스트를 보여줍니다.)\n` }
}

/** Mirrors `crates/sc-core/src/ops.rs::write_text`: an existing file demands
 *  a matching `if_match`, a not-yet-existing one demands none at all. Either
 *  mismatch is a `412 fs.precondition` carrying the server's current etag,
 *  exactly like the real backend. */
async function writeFile(path: string, content: string, ifMatch?: string): Promise<Entry> {
  await delay(30)
  const n = normalizePath(path)
  const parent = parentOf(n)
  const name = baseName(n)
  const existing = resolveDirEntries(parent).find((e) => e.name === name)
  if (existing) {
    if (ifMatch !== existing.etag) {
      throw new ApiError(412, {
        code: 'fs.precondition',
        message: 'If-Match precondition failed',
        detail: { current_etag: existing.etag }
      })
    }
  } else if (ifMatch) {
    throw new ApiError(412, {
      code: 'fs.precondition',
      message: 'If-Match precondition failed',
      detail: { current_etag: '' }
    })
  }
  const nowNs = (BigInt(Date.now()) * 1_000_000n).toString()
  const updated: Entry = existing
    ? { ...existing, etag: randomId('e'), size: content.length, mtime_ns: nowNs }
    : {
        name,
        kind: 'file',
        size: content.length,
        mtime_ns: nowNs,
        etag: randomId('e'),
        perms: defaultPerms(),
        id: newMockFileId()
      }
  fileContents.set(n, content)
  addOverlayEntry(parent, updated)
  return updated
}

// ── auth (login/session/logout) — ──
// Defaults to "already logged in" so every existing dev workflow (and every
// other test in this file) is unaffected; only an explicit logout() flips
// it, which is how the login screen becomes reachable in mock mode without
// disturbing anything else.
const DEMO_USER = { name: 'demo', password: 'password12' }
const DEMO_TOTP_USER = { name: 'totp-demo', password: 'password12', code: '123456' }

/** sessionStorage key shared (by convention, not by import — see
 *  api/setup.ts's header comment) with the mock branch of
 *  `setup.ts::createInitialAdmin`, so a dev-mode first-run account can log
 *  back in with the exact credentials it was created with. */
const MOCK_SETUP_ADMIN_KEY = 'sc.mock.setup_admin'

const mockAuthState: {
  loggedIn: boolean
  pendingChallenge: string | null
  password: string
  totpEnabled: boolean
  smbOptOut: boolean
  smbEnabled: boolean
  pendingTotpSecret: string | null
  /** — unused recovery codes left. Zero while TOTP is
   *  off (mirrors the server: `totp_disable` deletes every row), reset to 10
   *  by `totpEnroll`/`reissueRecoveryCodes`. */
  recoveryCodesRemaining: number
  /** Server-global chunk floor/default (`PATCH /api/admin/upload-settings`) — mirrors the real server's
   *  `upload.db`-persisted value read by every `session()` call. */
  chunkMin: number
  chunkDefault: number
  /** — the name index's persisted runtime override,
   *  off by default same as the real server. */
  indexNameEnabled: boolean
  /** The demo account starts *linked* (`docs/proposals/stowcloud-0-oidc-login.md`
   *  §4.3.2). That is the state worth having available without a server: it
   *  carries the destructive action (disconnecting, which revokes sessions and
   *  re-derives the SMB hash) and it round-trips fully in the mock. The
   *  connect direction cannot, since there is no identity provider to send a
   *  browser to. */
  oidcLinked: boolean
} = {
  loggedIn: true,
  pendingChallenge: null,
  password: DEMO_USER.password,
  totpEnabled: false,
  smbOptOut: false,
  smbEnabled: true,
  pendingTotpSecret: null,
  recoveryCodesRemaining: 0,
  chunkMin: 5 * 1024 * 1024,
  chunkDefault: 10 * 1024 * 1024,
  indexNameEnabled: false,
  oidcLinked: true
}

function mockSetupAdmin(): { username: string; password: string } | null {
  try {
    const raw = sessionStorage.getItem(MOCK_SETUP_ADMIN_KEY)
    return raw ? JSON.parse(raw) : null
  } catch {
    return null
  }
}

async function login(username: string, password: string): Promise<LoginResult> {
  await delay(150)
  const created = mockSetupAdmin()
  if (created && created.username === username && created.password === password) {
    mockAuthState.loggedIn = true
    return { status: 'ok', user: { id: 1, name: username } }
  }
  if (username === DEMO_USER.name && password === mockAuthState.password) {
    mockAuthState.loggedIn = true
    return { status: 'ok', user: { id: 1, name: username } }
  }
  if (username === DEMO_TOTP_USER.name && password === DEMO_TOTP_USER.password) {
    const challenge = randomId('chal')
    mockAuthState.pendingChallenge = challenge
    return { status: 'totp_required', challenge }
  }
  throw new ApiError(401, { code: 'auth.invalid_credentials', message: 'invalid credentials' })
}

async function loginTotp(challenge: string, code: string): Promise<LoginResult> {
  await delay(100)
  if (mockAuthState.pendingChallenge && challenge === mockAuthState.pendingChallenge && code === DEMO_TOTP_USER.code) {
    mockAuthState.loggedIn = true
    mockAuthState.pendingChallenge = null
    return { status: 'ok', user: { id: 2, name: DEMO_TOTP_USER.name } }
  }
  throw new ApiError(401, { code: 'auth.invalid_credentials', message: 'invalid credentials' })
}

async function logout(): Promise<void> {
  await delay(10)
  mockAuthState.loggedIn = false
  mockAuthState.pendingChallenge = null
}

async function session(): Promise<SessionInfo> {
  await delay(10)
  if (!mockAuthState.loggedIn) {
    throw new ApiError(401, { code: 'auth.required', message: 'authentication required' })
  }
  return {
    user: {
      id: 1,
      name: 'demo',
      display_name: '데모 사용자',
      is_admin: true,
      totp_enabled: mockAuthState.totpEnabled,
      smb_opt_out: mockAuthState.smbOptOut,
      smb_enabled: mockAuthState.smbEnabled
    },
    roots: [
      { label: 'home', perms: defaultPerms(), share_kind: 'Home', shared_externally: false, trash_enabled: false }
    ],
    csrf: 'mock-csrf-token',
    limits: { chunk_size: mockAuthState.chunkDefault, chunk_min: mockAuthState.chunkMin, max_file_size: null, parallel: 4 },
    features: {
      webdav: true,
      smb: false,
      preview: true,
      trash: true,
      shares: true,
      search: 'name'
    },
    oidc: mockAuthState.oidcLinked
      ? { linked: true, subject_hint: MOCK_OIDC_SUBJECT_HINT, linked_ns: MOCK_OIDC_LINKED_NS }
      : { linked: false }
  }
}

// ── single sign-on (`docs/proposals/stowcloud-0-oidc-login.md` §4.3.2) ──
//
// `GET /api/auth/oidc/config` is not here: it has to answer before there is a
// session, so it lives in the standalone `api/oidc.ts`, which serves its own
// mock value.

/** A Keycloak-shaped subject, hinted the way the server hints it: four
 *  characters from each end and nothing in between. */
const MOCK_OIDC_SUBJECT = 'f81d4fae-7dec-11d0-a765-00a0c91e6bf6'
const MOCK_OIDC_SUBJECT_HINT = 'f81d...6bf6'
const MOCK_OIDC_LINKED_NS = String(BigInt(Date.UTC(2026, 6, 1)) * 1_000_000n)

/**
 * Refuses with `oidc.provider_unavailable` *after* checking the password.
 *
 * There is no identity provider behind the mock, and the honest failure is the
 * one the real server gives when it cannot reach one. The alternative would be
 * to hand back an invented `authorize_url`, which the caller then navigates
 * to, taking the browser out of the app entirely.
 */
async function oidcLinkStart(password: string, _returnTo?: string): Promise<{ authorize_url: string }> {
  await delay(80)
  if (password !== mockAuthState.password) {
    throw new ApiError(401, { code: 'auth.invalid_credentials', message: 'invalid credentials' })
  }
  throw new ApiError(503, { code: 'oidc.provider_unavailable', message: 'no identity provider in the mock backend' })
}

async function oidcUnlink(password: string): Promise<void> {
  await delay(80)
  if (password !== mockAuthState.password) {
    throw new ApiError(401, { code: 'auth.invalid_credentials', message: 'invalid credentials' })
  }
  if (!mockAuthState.oidcLinked) {
    throw new ApiError(404, { code: 'oidc.not_linked', message: 'no linked identity' })
  }
  mockAuthState.oidcLinked = false
}

/** Which mock accounts have an identity attached. Keyed by the same ids
 *  `adminListUsers` hands out. */
const mockUserOidc = new Map<number, string>([[1, MOCK_OIDC_SUBJECT]])

async function adminGetUserOidc(id: number): Promise<AdminUserOidc> {
  await delay(30)
  const subject = mockUserOidc.get(id)
  if (!subject) {
    return { linked: false, issuer: null, subject: null, linked_ns: null, last_login_ns: null }
  }
  return {
    linked: true,
    issuer: 'https://idp.example.com/realms/mock',
    subject,
    linked_ns: MOCK_OIDC_LINKED_NS,
    last_login_ns: MOCK_OIDC_LINKED_NS
  }
}

async function adminLinkUserOidc(id: number, subject: string): Promise<void> {
  await delay(60)
  const trimmed = subject.trim()
  if (!trimmed) {
    throw new ApiError(422, { code: 'oidc.invalid_subject', message: 'subject must not be empty' })
  }
  for (const [other, s] of mockUserOidc) {
    if (other !== id && s === trimmed) {
      throw new ApiError(409, { code: 'oidc.subject_already_linked', message: 'that identity belongs to another account' })
    }
  }
  const existing = mockUserOidc.get(id)
  if (existing && existing !== trimmed) {
    throw new ApiError(409, { code: 'oidc.already_linked', message: 'this account already has a linked identity' })
  }
  mockUserOidc.set(id, trimmed)
  if (id === 1) mockAuthState.oidcLinked = true
}

async function adminUnlinkUserOidc(id: number): Promise<AdminOidcUnlinkResult> {
  await delay(60)
  if (!mockUserOidc.has(id)) {
    throw new ApiError(404, { code: 'oidc.not_linked', message: 'no linked identity' })
  }
  mockUserOidc.delete(id)
  if (id === 1) mockAuthState.oidcLinked = false
  // `smb_nt_restored` is always false on this route, mock or not: an admin
  // unlink has no plaintext password to re-derive the NT hash from (§4.3.6).
  return {
    smb_nt_restored: false,
    oidc_sessions_revoked: 0
  }
}

// ── settings ──
// Mirrors http.ts's real-server surface so the settings screens behave
// identically under `VITE_API_MOCK=1` and against the real backend — the
// same convention as every other function in this file.

let mockAppPasswords: AppPasswordInfo[] = []
let nextAppPasswordId = 1

const MOCK_SESSION_ID_HASH = 'mock-current-session'
let mockSessions: ActiveSession[] = [
  {
    id_hash: MOCK_SESSION_ID_HASH,
    created_ns: String(BigInt(Date.now()) * 1_000_000n),
    last_seen_ns: String(BigInt(Date.now()) * 1_000_000n),
    absolute_expiry_ns: String(BigInt(Date.now() + 30 * 24 * 3600 * 1000) * 1_000_000n),
    ip_first: '127.0.0.1',
    ua_first: 'Mock/1.0',
    current: true
  }
]

async function changePassword(currentPassword: string, newPassword: string, revokeOtherSessions: boolean): Promise<void> {
  await delay(80)
  if (currentPassword !== mockAuthState.password) {
    throw new ApiError(401, { code: 'auth.invalid_credentials', message: 'invalid credentials' })
  }
  if (newPassword.length < 10) {
    throw new ApiError(422, { code: 'auth.weak_password', message: 'password is too short', detail: { min_length: 10 } })
  }
  mockAuthState.password = newPassword
  if (revokeOtherSessions) {
    mockSessions = mockSessions.filter((s) => s.current)
  }
}

async function totpSetup(): Promise<{ secret: string; otpauth_url: string }> {
  await delay(30)
  // A real base32 secret so a user who copies it into an authenticator app
  // gets a working (if mock-unverified) entry — only the *server-side*
  // check is stubbed out below.
  const secret = 'JBSWY3DPEHPK3PXP'
  mockAuthState.pendingTotpSecret = secret
  return { secret, otpauth_url: `otpauth://totp/Stowcloud:demo?secret=${secret}&issuer=Stowcloud` }
}

async function totpEnroll(password: string, secret: string, code: string): Promise<{ recovery_codes: string[] }> {
  await delay(80)
  if (password !== mockAuthState.password || secret !== mockAuthState.pendingTotpSecret) {
    throw new ApiError(401, { code: 'auth.invalid_credentials', message: 'invalid credentials' })
  }
  // No TOTP/HMAC library in the mock: any syntactically-plausible 6-digit
  // code is accepted, mirroring how `DEMO_TOTP_USER` already hardcodes a
  // fixed login code elsewhere in this file rather than computing one.
  if (!/^\d{6}$/.test(code)) {
    throw new ApiError(401, { code: 'auth.invalid_credentials', message: 'invalid credentials' })
  }
  mockAuthState.totpEnabled = true
  mockAuthState.pendingTotpSecret = null
  mockAuthState.recoveryCodesRemaining = 10
  return { recovery_codes: Array.from({ length: 10 }, (_, i) => `MOCK${i}CODE7X`) }
}

async function totpDisable(password: string): Promise<void> {
  await delay(80)
  if (password !== mockAuthState.password) {
    throw new ApiError(401, { code: 'auth.invalid_credentials', message: 'invalid credentials' })
  }
  mockAuthState.totpEnabled = false
  mockAuthState.recoveryCodesRemaining = 0
}

/** Mock counterpart of `http.ts`'s function of the same name. */
async function recoveryCodesRemaining(): Promise<{ remaining: number }> {
  await delay(15)
  return { remaining: mockAuthState.recoveryCodesRemaining }
}

/** Mock counterpart of `http.ts`'s function of the same name — same
 *  password re-confirmation, same "replaces the whole set" semantics. */
async function reissueRecoveryCodes(password: string): Promise<{ recovery_codes: string[] }> {
  await delay(80)
  if (password !== mockAuthState.password) {
    throw new ApiError(401, { code: 'auth.invalid_credentials', message: 'invalid credentials' })
  }
  if (!mockAuthState.totpEnabled) {
    throw new ApiError(409, { code: 'auth.totp_not_enabled', message: 'totp is not enabled' })
  }
  mockAuthState.recoveryCodesRemaining = 10
  const stamp = randomId('r')
  return { recovery_codes: Array.from({ length: 10 }, (_, i) => `MOCK${i}${stamp}`.slice(0, 10).toUpperCase()) }
}

async function listAppPasswords(): Promise<AppPasswordInfo[]> {
  await delay(20)
  return mockAppPasswords.slice()
}

async function createAppPassword(name: string): Promise<{ id: number; token: string }> {
  await delay(50)
  const id = nextAppPasswordId++
  mockAppPasswords = [
    { id, name, created_ns: String(BigInt(Date.now()) * 1_000_000n), last_used_ns: null, expires_ns: null },
    ...mockAppPasswords
  ]
  return { id, token: `stow_mock-${id}-${randomId('tok')}` }
}

/** Mock counterpart of http.ts's seam of the same name — see that file for
 *  why this exists and what will change once the real endpoint lands. */
async function createScopedAppPassword(name: string): Promise<{ id: number; token: string }> {
  await delay(50)
  const id = nextAppPasswordId++
  mockAppPasswords = [
    { id, name, created_ns: String(BigInt(Date.now()) * 1_000_000n), last_used_ns: null, expires_ns: null, read_only: true },
    ...mockAppPasswords
  ]
  return { id, token: `stow_mock-ro-${id}-${randomId('tok')}` }
}

async function revokeAppPassword(id: number): Promise<void> {
  await delay(30)
  const before = mockAppPasswords.length
  mockAppPasswords = mockAppPasswords.filter((p) => p.id !== id)
  if (mockAppPasswords.length === before) {
    throw new ApiError(404, { code: 'fs.not_found', message: 'not found' })
  }
}

async function listSessions(): Promise<ActiveSession[]> {
  await delay(20)
  return mockSessions.slice()
}

async function revokeSession(idHash: string): Promise<void> {
  await delay(30)
  const before = mockSessions.length
  mockSessions = mockSessions.filter((s) => s.id_hash !== idHash)
  if (mockSessions.length === before) {
    throw new ApiError(404, { code: 'fs.not_found', message: 'not found' })
  }
}

async function updateSmbSettings(optOut: boolean, enabled: boolean): Promise<void> {
  await delay(30)
  mockAuthState.smbOptOut = optOut
  mockAuthState.smbEnabled = enabled
}

async function adminStorage(): Promise<StorageReport> {
  await delay(20)
  return {
    db_bytes: 12_582_912,
    shares: [
      { label: 'home', free_bytes: 400_000_000_000, total_bytes: 500_000_000_000 }
    ]
  }
}

/** `PATCH /api/admin/upload-settings` — mirrors
 *  `UploadEngine::set_chunk_settings`'s validation: floor at `CHUNK_SIZE_MIN`,
 *  default must not be below min. Mutates `mockAuthState` so every
 *  subsequent `session()` call (any tab, since this is one shared module
 *  instance) reports the new value, same as the real server's global write. */
async function adminSetUploadSettings(req: UploadSettingsReq): Promise<UploadSettingsResp> {
  await delay(30)
  if (req.chunk_min < CHUNK_SIZE_MIN) {
    throw new ApiError(422, {
      code: 'fs.invalid_name',
      message: 'chunk_min too small',
      detail: { reason: `chunk_min must be at least ${CHUNK_SIZE_MIN} bytes` }
    })
  }
  if (req.chunk_default < req.chunk_min) {
    throw new ApiError(422, {
      code: 'fs.invalid_name',
      message: 'chunk_default below chunk_min',
      detail: { reason: 'chunk_default must be >= chunk_min' }
    })
  }
  mockAuthState.chunkMin = req.chunk_min
  mockAuthState.chunkDefault = req.chunk_default
  return { chunk_min: req.chunk_min, chunk_default: req.chunk_default }
}

// ── server settings (`crates/sc-http/src/settings_api.rs`) — mirrors
// `http.ts`'s real-server surface, same convention as every other section of
// this file. Field keys match `settings_bridge.rs`'s dotted `config.toml`
// paths exactly, since `ServerSettingsSection.svelte` groups by those literal
// strings regardless of which backend answered them. ──

const mockServerSettings = {
  smb: {
    enabled: false,
    workgroup: 'WORKGROUP',
    service_user: 'sc-smb',
    allow_public_bind: false,
    totp_policy: 'require_separate' as 'require_separate' | 'block',
    service_uid: 1000,
    service_gid: 1000
  },
  search: {
    max_concurrent_fast: 4,
    max_concurrent_slow: 2,
    walk_deadline_fast_ms: 100,
    walk_deadline_slow_ms: 500,
    rate_per_minute: 30
  },
  archive: { max_concurrent: 2 },
  network: {
    bind: '127.0.0.1:8080',
    app_hosts: [] as string[],
    content_hosts: [] as string[],
    allowed_origins: [] as string[],
    trusted_proxies: [] as string[],
    compat_canonical_url: null as string | null
  },
  db: { size_guard: true, max_bytes: 5_000_000_000, min_free_bytes: 1_000_000_000 },
  symlink_policy: 'deny' as 'deny' | 'within_share' | 'follow',
  homes: { enabled: false, root: null as string | null },
  watch: {
    backend: 'auto' as 'auto' | 'hotset' | 'inotify_full' | 'fanotify',
    hot_set_max: 4096,
    full_threshold: 50_000
  },
  paths: {
    data_dir: '/var/lib/stowcloud',
    master_key_file: null as string | null,
    smb_config_dir: '/etc/stowcloud/smb'
  },
  oidc: {
    enabled: false,
    issuer: '',
    client_id: '',
    redirect_uri: '',
    scopes: ['openid', 'profile'] as string[],
    display_name: '',
    allow_private_endpoints: false,
    smb_policy: 'block' as const
  }
}

function settingsField(key: string, value: unknown, restartRequired: boolean): SettingsField {
  return { key, value, source: 'admin_override', restart_required: restartRequired, readonly_reason_key: null }
}

async function adminGetServerSettings(): Promise<SettingsSnapshot> {
  await delay(20)
  const s = mockServerSettings
  return {
    fields: [
      settingsField('bind', s.network.bind, true),
      settingsField('app_hosts', s.network.app_hosts, true),
      settingsField('content_hosts', s.network.content_hosts, true),
      settingsField('allowed_origins', s.network.allowed_origins, true),
      settingsField('trusted_proxies', s.network.trusted_proxies, true),
      settingsField('compat_canonical_url', s.network.compat_canonical_url, true),
      settingsField('db.size_guard', s.db.size_guard, true),
      settingsField('db.max_bytes', s.db.max_bytes, true),
      settingsField('db.min_free_bytes', s.db.min_free_bytes, true),
      settingsField('symlink_policy', s.symlink_policy, true),
      settingsField('homes.enabled', s.homes.enabled, true),
      settingsField('homes.root', s.homes.root, true),
      settingsField('smb.enabled', s.smb.enabled, true),
      settingsField('smb.workgroup', s.smb.workgroup, false),
      settingsField('smb.service_user', s.smb.service_user, false),
      settingsField('smb.allow_public_bind', s.smb.allow_public_bind, false),
      settingsField('smb.totp_policy', s.smb.totp_policy, false),
      settingsField('smb.service_uid', s.smb.service_uid, false),
      settingsField('smb.service_gid', s.smb.service_gid, false),
      settingsField('search.max_concurrent_fast', s.search.max_concurrent_fast, false),
      settingsField('search.max_concurrent_slow', s.search.max_concurrent_slow, false),
      settingsField('search.walk_deadline_fast_ms', s.search.walk_deadline_fast_ms, false),
      settingsField('search.walk_deadline_slow_ms', s.search.walk_deadline_slow_ms, false),
      settingsField('search.rate_per_minute', s.search.rate_per_minute, false),
      settingsField('archive.max_concurrent', s.archive.max_concurrent, false),
      settingsField('watch.backend', s.watch.backend, true),
      settingsField('watch.hot_set_max', s.watch.hot_set_max, true),
      settingsField('watch.full_threshold', s.watch.full_threshold, true),
      settingsField('data_dir', s.paths.data_dir, true),
      settingsField('master_key_file', s.paths.master_key_file, true),
      settingsField('smb.config_dir', s.paths.smb_config_dir, true),
      settingsField('oidc.enabled', s.oidc.enabled, true),
      settingsField('oidc.issuer', s.oidc.issuer, true),
      settingsField('oidc.client_id', s.oidc.client_id, true),
      settingsField('oidc.redirect_uri', s.oidc.redirect_uri, true),
      settingsField('oidc.scopes', s.oidc.scopes, true),
      settingsField('oidc.display_name', s.oidc.display_name, true),
      settingsField('oidc.allow_private_endpoints', s.oidc.allow_private_endpoints, true),
      settingsField('oidc.smb_policy', s.oidc.smb_policy, true),
      // The two `config.toml` owns outright (§6-4). They carry their reason
      // here the same way the real bridge writes it, so the screen's
      // read-only list is not empty under the mock.
      {
        key: 'oidc.client_secret_file',
        value: null,
        source: 'config_file',
        restart_required: true,
        readonly_reason_key: 'settings.readonly_secret_file_path'
      },
      {
        key: 'oidc.local_password_login',
        value: 'allow',
        source: 'builtin_default',
        restart_required: true,
        readonly_reason_key: 'settings.readonly_local_password_login'
      }
    ],
    smb_public_bind_warning: false,
    smb_overgrants: []
  }
}

async function adminSetSmbSettings(req: SmbSettingsReq): Promise<ApplyOutcome> {
  await delay(30)
  const enabledChanged = mockServerSettings.smb.enabled !== req.enabled
  mockServerSettings.smb = { ...req }
  return { applied_live: !enabledChanged, restart_required: enabledChanged }
}

async function adminSetSearchSettings(req: SearchSettingsReq): Promise<ApplyOutcome> {
  await delay(30)
  mockServerSettings.search = { ...req }
  return { applied_live: true, restart_required: false }
}

async function adminSetArchiveSettings(req: ArchiveSettingsReq): Promise<ApplyOutcome> {
  await delay(30)
  mockServerSettings.archive = { ...req }
  return { applied_live: true, restart_required: false }
}

async function adminSetNetworkSettings(req: NetworkSettingsReq): Promise<ApplyOutcome> {
  await delay(30)
  mockServerSettings.network = { ...req }
  return { applied_live: false, restart_required: true }
}

async function adminSetDbSettings(req: DbSettingsReq): Promise<ApplyOutcome> {
  await delay(30)
  mockServerSettings.db = { ...req }
  return { applied_live: false, restart_required: true }
}

async function adminSetSymlinkPolicySettings(req: SymlinkPolicyReq): Promise<ApplyOutcome> {
  await delay(30)
  mockServerSettings.symlink_policy = req.policy
  return { applied_live: false, restart_required: true }
}

async function adminSetHomesSettings(req: HomesSettingsReq): Promise<ApplyOutcome> {
  await delay(30)
  mockServerSettings.homes = { ...req }
  return { applied_live: false, restart_required: true }
}

async function adminSetWatchSettings(req: WatchSettingsReq): Promise<ApplyOutcome> {
  await delay(30)
  if (req.hot_set_max < 1 || req.full_threshold < 1) {
    throw new ApiError(422, {
      code: 'fs.invalid_name',
      message: 'invalid name',
      detail: {
        reason: 'watch.hot_set_max: must be at least 1',
        reason_key: 'settings.must_be_at_least_one',
        reason_params: { field: 'watch.hot_set_max' }
      }
    })
  }
  mockServerSettings.watch = { ...req }
  return { applied_live: false, restart_required: true }
}

/** Refuses a `redirect_uri` that is not `https://` exactly as the real bridge
 *  does, because that one is not a filesystem check: an empty or non-https
 *  value is what keeps OIDC switched off with everything else still running
 *  (§4.3.1), and a screen that only finds out at the next boot is a screen
 *  that lies. */
async function adminSetOidcSettings(req: OidcSettingsReq): Promise<ApplyOutcome> {
  await delay(30)
  if (req.enabled && !req.redirect_uri.startsWith('https://')) {
    throw new ApiError(422, {
      code: 'fs.invalid_name',
      message: 'invalid name',
      detail: {
        reason: 'oidc.redirect_uri: must start with https://',
        reason_key: 'settings.oidc_redirect_uri_must_be_https',
        reason_params: { value: req.redirect_uri }
      }
    })
  }
  mockServerSettings.oidc = { ...req, smb_policy: 'block' }
  return { applied_live: false, restart_required: true }
}

/** The real server also refuses paths that do not exist, a `data_dir` without
 *  `auth.db`, and a `master_key_file` holding different bytes — all
 *  filesystem checks the mock has no way to make. Absoluteness is the one
 *  refusal it can reproduce, so it is the one it reproduces. */
async function adminSetPathsSettings(req: PathsSettingsReq): Promise<ApplyOutcome> {
  await delay(30)
  const abs = (p: string) => p.startsWith('/')
  if (!abs(req.data_dir) || !abs(req.smb_config_dir) || (req.master_key_file && !abs(req.master_key_file))) {
    throw new ApiError(422, {
      code: 'fs.invalid_name',
      message: 'invalid name',
      detail: {
        reason: 'data_dir: must be an absolute path',
        reason_key: 'settings.path_must_be_absolute',
        reason_params: { field: 'data_dir', path: req.data_dir }
      }
    })
  }
  mockServerSettings.paths = {
    data_dir: req.data_dir,
    master_key_file: req.master_key_file,
    smb_config_dir: req.smb_config_dir
  }
  return { applied_live: false, restart_required: true }
}

/** The mock backend never has uploads/jobs in flight, so `force` is accepted
 *  but never actually needed — the busy-refusal path is only exercisable
 *  against the real server. */
async function adminRestartServer(_force: boolean): Promise<void> {
  await delay(30)
}

async function adminIndexEstimate(): Promise<IndexEstimate> {
  await delay(300)
  return {
    files: 214_882,
    index_bytes: 5_372_000,
    build_secs: 42,
    confidence: 'medium'
  }
}

async function adminIndexSettings(): Promise<IndexSettings> {
  await delay(20)
  return { name_enabled: mockAuthState.indexNameEnabled }
}

async function adminSetIndexSettings(nameEnabled: boolean): Promise<IndexSettings> {
  await delay(30)
  mockAuthState.indexNameEnabled = nameEnabled
  return { name_enabled: nameEnabled }
}

/** One fixed id, unlike the real server which mints a fresh one per build —
 *  this mock only ever has one build in flight (`StorageIndexSection.svelte`
 *  disables the button while `jobTray` already tracks it), and `jobStatus`
 *  below needs a stable id to recognize. Progress is a function of elapsed
 *  time rather than a real crawl, since there is no mock filesystem large
 *  enough for `CrawlThrottle` pacing to mean anything. */
const MOCK_INDEX_BUILD_JOB = 'mock-index-build'
const MOCK_INDEX_BUILD_SHARES = 3
const MOCK_INDEX_BUILD_MS_PER_SHARE = 900
let mockIndexBuildStartedAt = 0

async function adminBuildIndex(): Promise<{ job: string }> {
  await delay(20)
  if (!mockAuthState.indexNameEnabled) {
    throw new ApiError(501, { code: 'not_implemented', message: 'not implemented yet' })
  }
  mockIndexBuildStartedAt = Date.now()
  return { job: MOCK_INDEX_BUILD_JOB }
}

// ── admin: user management ──
// Mirrors the real server's rules closely enough to drive the admin UI in
// dev mode: the bootstrapped account is the sole administrator, a name must
// be unique, a password needs 10 characters, and the last active admin can
// be neither disabled nor deleted (`sc_auth::AdminGuardError::LastAdmin`).

let mockUsers: AdminUser[] = [
  {
    id: 1,
    name: 'demo',
    display_name: '데모 사용자',
    is_admin: true,
    disabled: false,
    totp_enabled: false,
    smb_enabled: true,
    created_ns: String(BigInt(Date.now()) * 1_000_000n),
    quota_bytes: null,
    usage_bytes: '0'
  }
]
let nextUserId = 2

function activeAdminCount(): number {
  return mockUsers.filter((u) => u.is_admin && !u.disabled).length
}

async function adminListUsers(): Promise<AdminUser[]> {
  await delay(20)
  return mockUsers.slice()
}

async function adminCreateUser(name: string, password: string): Promise<AdminUser> {
  await delay(60)
  if (password.length < 10) {
    throw new ApiError(422, { code: 'auth.weak_password', message: 'password is too short', detail: { min_length: 10 } })
  }
  if (mockUsers.some((u) => u.name.toLowerCase() === name.toLowerCase())) {
    throw new ApiError(409, { code: 'fs.conflict', message: 'a user with that name already exists' })
  }
  const user: AdminUser = {
    id: nextUserId++,
    name,
    display_name: name,
    is_admin: false,
    disabled: false,
    totp_enabled: false,
    smb_enabled: true,
    created_ns: String(BigInt(Date.now()) * 1_000_000n),
    quota_bytes: null,
    usage_bytes: '0'
  }
  mockUsers = [...mockUsers, user]
  return user
}

async function adminSetUserDisabled(id: number, disabled: boolean): Promise<AdminUser> {
  await delay(40)
  const user = mockUsers.find((u) => u.id === id)
  if (!user) {
    throw new ApiError(404, { code: 'fs.not_found', message: 'not found' })
  }
  if (disabled && user.is_admin && !user.disabled && activeAdminCount() <= 1) {
    throw new ApiError(409, { code: 'admin.last_admin', message: 'refusing to remove the last administrator' })
  }
  user.disabled = disabled
  mockUsers = mockUsers.map((u) => (u.id === id ? { ...user } : u))
  return { ...user }
}

/** Mirrors the real `422 admin.invalid_quota` refusal for `0`
 * — `0` reads as unlimited downstream, so it is
 *  rejected rather than silently accepted. */
async function adminSetUserQuota(id: number, quotaBytes: number | null): Promise<AdminUser> {
  await delay(40)
  const user = mockUsers.find((u) => u.id === id)
  if (!user) {
    throw new ApiError(404, { code: 'fs.not_found', message: 'not found' })
  }
  if (quotaBytes === 0) {
    throw new ApiError(422, { code: 'admin.invalid_quota', message: 'quota must be greater than zero, or null for unlimited' })
  }
  user.quota_bytes = quotaBytes === null ? null : String(quotaBytes)
  mockUsers = mockUsers.map((u) => (u.id === id ? { ...user } : u))
  return { ...user }
}

async function adminDeleteUser(id: number): Promise<void> {
  await delay(40)
  const user = mockUsers.find((u) => u.id === id)
  if (!user) {
    throw new ApiError(404, { code: 'fs.not_found', message: 'not found' })
  }
  if (user.is_admin && !user.disabled && activeAdminCount() <= 1) {
    throw new ApiError(409, { code: 'admin.last_admin', message: 'refusing to remove the last administrator' })
  }
  mockUsers = mockUsers.filter((u) => u.id !== id)
}

// ── admin: share and grant management (`GET/POST /api/admin/shares`,
// `PATCH/DELETE /api/admin/shares/{id}`, `GET/POST /api/admin/grants`,
// `PATCH/DELETE /api/admin/grants/{id}`) ── Mirrors `sc_core::share`/
// `sc_core::acl_store` closely enough to drive the admin UI in dev mode:
// no-access-by-default, a grant needs at least one `allow` or `deny` bit,
// `subpath`/`share`/`principal` are immutable once created (delete and
// recreate instead — same rule `sc_core::acl_store::GrantPatch` enforces
// server-side), and a config-file share (`config_defined: true`) takes an
// edit but refuses a delete, the same way `sc_core::Core::update_share`/
// `delete_share` do.
// The real endpoints now exist
// (`crates/sc-http/src/routes.rs::admin_list_shares`/`admin_create_share`/
// `admin_update_share`/`admin_delete_share`/`admin_list_grants`/
// `admin_create_grant`/`admin_update_grant`/`admin_delete_grant`); this mock
// stays in sync with their wire shapes and error codes so
// `ShareManagementSection.svelte`/`GrantManagementSection.svelte` behave
// identically against either backend.

// The mock backend models one flat virtual tree (`STATIC_SEED`'s `/` listing),
// not the real server's per-share roots — there is no mock equivalent of
// `sc_core::Core::share_defs()` to derive this from. A small fixed list
// matching the top-level folders `STATIC_SEED` already seeds is enough to
// demo the grant-creation screen's share picker.
let mockShares: AdminShare[] = [
  { id: 1, name: 'Documents', host_path: '/srv/documents', config_defined: true, trash_enabled: false },
  { id: 2, name: 'Photos', host_path: '/srv/photos', config_defined: true, trash_enabled: false },
  { id: 3, name: 'Videos', host_path: '/srv/videos', config_defined: true, trash_enabled: false },
  { id: 4, name: 'Music', host_path: '/srv/music', config_defined: true, trash_enabled: false }
]
let nextShareId = 1_000_000 // mirrors `sc_core::DYNAMIC_SHARE_ID_BASE`

let mockGrants: AdminGrant[] = []
let nextGrantId = 1

async function adminListShares(): Promise<AdminShare[]> {
  await delay(15)
  return mockShares.slice()
}

/** No real filesystem to check `host_path` against in mock mode, so this
 *  only reproduces the checks that don't need one: empty/duplicate name and
 *  an overlapping `host_path` — the real backend's nonexistent-path/
 *  not-a-directory/unreadable checks (`sc_core::share::validate_host_path`)
 *  have no mock equivalent. */
async function adminCreateShare(req: CreateShareReq): Promise<AdminShare> {
  await delay(40)
  const name = req.name.trim()
  if (name === '') {
    throw new ApiError(422, { code: 'fs.invalid_name', message: 'share name must not be empty' })
  }
  if (mockShares.some((s) => s.name === name)) {
    throw new ApiError(422, { code: 'fs.invalid_name', message: `a share named '${name}' already exists` })
  }
  if (mockShares.some((s) => s.host_path === req.host_path)) {
    throw new ApiError(422, { code: 'fs.invalid_name', message: `overlaps existing share` })
  }
  const share: AdminShare = { id: nextShareId++, name, host_path: req.host_path, config_defined: false, trash_enabled: false }
  mockShares = [...mockShares, share]
  return share
}

/** A config-file share takes an edit like any other: the real backend keeps
 *  the new name/path (and the trash toggle) in `shares.db` rather than
 *  `config.toml` and reapplies them at startup, so nothing is lost on
 *  restart (`sc_core::Core::update_share`). */
async function adminUpdateShare(id: number, patch: UpdateShareReq): Promise<AdminShare> {
  await delay(35)
  const share = mockShares.find((s) => s.id === id)
  if (!share) {
    throw new ApiError(404, { code: 'fs.not_found', message: 'not found' })
  }
  const nextName = patch.name !== undefined ? patch.name.trim() : share.name
  if (nextName === '') {
    throw new ApiError(422, { code: 'fs.invalid_name', message: 'share name must not be empty' })
  }
  if (mockShares.some((s) => s.id !== id && s.name === nextName)) {
    throw new ApiError(422, { code: 'fs.invalid_name', message: `a share named '${nextName}' already exists` })
  }
  const updated: AdminShare = {
    ...share,
    name: nextName,
    host_path: patch.host_path ?? share.host_path,
    trash_enabled: patch.trash_enabled ?? share.trash_enabled
  }
  mockShares = mockShares.map((s) => (s.id === id ? updated : s))
  return updated
}

async function adminDeleteShare(id: number): Promise<void> {
  await delay(30)
  const share = mockShares.find((s) => s.id === id)
  if (!share) {
    throw new ApiError(404, { code: 'fs.not_found', message: 'not found' })
  }
  if (share.config_defined) {
    throw new ApiError(422, {
      code: 'fs.invalid_name',
      message: 'this share is defined in the config file and cannot be deleted here; edit the config and restart'
    })
  }
  mockShares = mockShares.filter((s) => s.id !== id)
  mockGrants = mockGrants.filter((g) => g.share !== id)
}

async function adminListGrants(opts: { userId?: number; groupId?: number; share?: number } = {}): Promise<AdminGrant[]> {
  await delay(20)
  return mockGrants.filter((g) => {
    if (opts.userId !== undefined && !(g.principal.kind === 'user' && g.principal.id === opts.userId)) return false
    if (opts.groupId !== undefined && !(g.principal.kind === 'group' && g.principal.id === opts.groupId)) return false
    if (opts.share !== undefined && g.share !== opts.share) return false
    return true
  })
}

async function adminCreateGrant(req: CreateGrantReq): Promise<AdminGrant> {
  await delay(50)
  if (req.allow.length === 0 && req.deny.length === 0) {
    throw new ApiError(422, {
      // Matches the real backend's code for this refusal exactly
      // (`sc_core::CoreError::InvalidPath` -> `hapi::CoreError::InvalidName`
      // -> `ErrorCode::FsInvalidName` -> `"fs.invalid_name"`,
      // `crates/sc-http/src/core_api.rs`) — this used to say
      // `fs.invalid_path`, a code the server has never actually sent, which
      // nothing caught because the real endpoint didn't exist yet either.
      code: 'fs.invalid_name',
      message: 'a grant must allow or deny at least one permission'
    })
  }
  if (!mockShares.some((s) => s.id === req.share)) {
    throw new ApiError(404, { code: 'fs.not_found', message: 'no such share' })
  }
  const share = mockShares.find((s) => s.id === req.share)!
  const grant: AdminGrant = {
    id: nextGrantId++,
    principal: req.principal,
    share: req.share,
    subpath: req.subpath,
    allow: req.allow,
    deny: req.deny,
    inherit: req.inherit,
    label: req.label ?? (req.subpath === '' ? share.name : req.subpath.split('/').pop() ?? share.name),
    created_ns: String(BigInt(Date.now()) * 1_000_000n)
  }
  mockGrants = [...mockGrants, grant]
  return grant
}

async function adminUpdateGrant(id: number, patch: UpdateGrantReq): Promise<AdminGrant> {
  await delay(40)
  const grant = mockGrants.find((g) => g.id === id)
  if (!grant) {
    throw new ApiError(404, { code: 'fs.not_found', message: 'not found' })
  }
  const nextAllow = patch.allow ?? grant.allow
  const nextDeny = patch.deny ?? grant.deny
  if (nextAllow.length === 0 && nextDeny.length === 0) {
    throw new ApiError(422, {
      code: 'fs.invalid_name',
      message: 'a grant must allow or deny at least one permission'
    })
  }
  const updated: AdminGrant = {
    ...grant,
    allow: nextAllow,
    deny: nextDeny,
    inherit: patch.inherit ?? grant.inherit,
    label: 'label' in patch ? (patch.label ?? null) : grant.label
  }
  mockGrants = mockGrants.map((g) => (g.id === id ? updated : g))
  return updated
}

async function adminDeleteGrant(id: number): Promise<void> {
  await delay(30)
  if (!mockGrants.some((g) => g.id === id)) {
    throw new ApiError(404, { code: 'fs.not_found', message: 'not found' })
  }
  mockGrants = mockGrants.filter((g) => g.id !== id)
}

// ── admin: group management ── Mirrors
// `sc_auth::AuthService`'s group CRUD closely enough to drive the admin UI in
// dev mode: `group_.name` is unique, deleting a group cascades to its
// memberships (and to the live grants table's own filter, same as deleting a
// share cascades to `mockGrants` above), and adding/removing a member refuses
// an unknown user id.

let mockGroups: AdminGroup[] = []
let nextGroupId = 1

async function adminListGroups(): Promise<AdminGroup[]> {
  await delay(15)
  return mockGroups.map((g) => ({ ...g, members: [...g.members] }))
}

async function adminCreateGroup(req: CreateGroupReq): Promise<AdminGroup> {
  await delay(40)
  const name = req.name.trim()
  if (name === '') {
    throw new ApiError(422, { code: 'fs.invalid_name', message: 'group name must not be empty' })
  }
  if (mockGroups.some((g) => g.name === name)) {
    throw new ApiError(409, { code: 'fs.conflict', message: 'a group with that name already exists' })
  }
  const group: AdminGroup = { id: nextGroupId++, name, members: [] }
  mockGroups = [...mockGroups, group]
  return { ...group, members: [] }
}

async function adminRenameGroup(id: number, patch: UpdateGroupReq): Promise<AdminGroup> {
  await delay(35)
  const group = mockGroups.find((g) => g.id === id)
  if (!group) {
    throw new ApiError(404, { code: 'fs.not_found', message: 'not found' })
  }
  const name = patch.name.trim()
  if (name === '') {
    throw new ApiError(422, { code: 'fs.invalid_name', message: 'group name must not be empty' })
  }
  if (mockGroups.some((g) => g.id !== id && g.name === name)) {
    throw new ApiError(409, { code: 'fs.conflict', message: 'a group with that name already exists' })
  }
  const updated: AdminGroup = { ...group, name }
  mockGroups = mockGroups.map((g) => (g.id === id ? updated : g))
  return { ...updated, members: [...updated.members] }
}

async function adminDeleteGroup(id: number): Promise<void> {
  await delay(30)
  if (!mockGroups.some((g) => g.id === id)) {
    throw new ApiError(404, { code: 'fs.not_found', message: 'not found' })
  }
  mockGroups = mockGroups.filter((g) => g.id !== id)
  mockGrants = mockGrants.filter((g) => !(g.principal.kind === 'group' && g.principal.id === id))
}

async function adminAddGroupMember(id: number, userId: number): Promise<void> {
  await delay(30)
  const group = mockGroups.find((g) => g.id === id)
  if (!group || !mockUsers.some((u) => u.id === userId)) {
    throw new ApiError(404, { code: 'fs.not_found', message: 'not found' })
  }
  if (!group.members.includes(userId)) {
    const updated: AdminGroup = { ...group, members: [...group.members, userId] }
    mockGroups = mockGroups.map((g) => (g.id === id ? updated : g))
  }
}

async function adminRemoveGroupMember(id: number, userId: number): Promise<void> {
  await delay(30)
  const group = mockGroups.find((g) => g.id === id)
  if (!group || !mockUsers.some((u) => u.id === userId)) {
    throw new ApiError(404, { code: 'fs.not_found', message: 'not found' })
  }
  const updated: AdminGroup = { ...group, members: group.members.filter((m) => m !== userId) }
  mockGroups = mockGroups.map((g) => (g.id === id ? updated : g))
}

// ── admin: audit log ──
// GET /api/admin/audit. A small fixed seed, newest first — enough for
// `AuditLogSection.svelte` to have something realistic to filter/paginate
// in dev mode. `actor_name` is resolved against `mockUsers` at read time
// (not baked into the seed), same as the real server's join.

interface MockAuditRow {
  rowid: number
  ts_ns: bigint
  actor: number | null
  event: string
  target: string | null
  ip: string | null
  ok: boolean
  detail: string | null
}

const auditNowNs = BigInt(Date.now()) * 1_000_000n
const mockAudit: MockAuditRow[] = [
  { rowid: 5, ts_ns: auditNowNs, actor: 1, event: 'auth.login', target: null, ip: '127.0.0.1', ok: true, detail: null },
  {
    rowid: 4,
    ts_ns: auditNowNs - 60_000_000_000n,
    actor: null,
    event: 'auth.login',
    target: null,
    ip: '203.0.113.7',
    ok: false,
    detail: 'bad password'
  },
  {
    rowid: 3,
    ts_ns: auditNowNs - 3_600_000_000_000n,
    actor: 1,
    event: 'admin.share.create',
    target: 'Documents',
    ip: '127.0.0.1',
    ok: true,
    detail: null
  },
  {
    rowid: 2,
    ts_ns: auditNowNs - 7_200_000_000_000n,
    actor: 1,
    event: 'admin.user.create',
    target: 'yuna',
    ip: '127.0.0.1',
    ok: true,
    detail: null
  },
  {
    rowid: 1,
    ts_ns: auditNowNs - 86_400_000_000_000n,
    actor: 1,
    event: 'auth.login',
    target: null,
    ip: '127.0.0.1',
    ok: true,
    detail: null
  }
]

async function adminListAudit(query: AuditQuery = {}): Promise<AuditPage> {
  await delay(20)
  const limit = Math.min(Math.max(query.limit ?? 50, 1), 200)
  const filtered = mockAudit.filter((r) => {
    if (query.actor !== undefined && r.actor !== query.actor) return false
    if (query.event !== undefined && r.event !== query.event) return false
    if (query.since_ns !== undefined && r.ts_ns < BigInt(query.since_ns)) return false
    if (query.until_ns !== undefined && r.ts_ns > BigInt(query.until_ns)) return false
    if (query.before !== undefined && r.rowid >= query.before) return false
    return true
  })
  const page = filtered.slice(0, limit)
  const rows: AuditRow[] = page.map((r) => ({
    rowid: r.rowid,
    ts_ns: r.ts_ns.toString(),
    actor: r.actor,
    actor_name: r.actor === null ? null : mockUsers.find((u) => u.id === r.actor)?.display_name ?? mockUsers.find((u) => u.id === r.actor)?.name ?? null,
    event: r.event,
    target: r.target,
    ip: r.ip,
    ok: r.ok,
    detail: r.detail
  }))
  const next = page.length === limit ? page[page.length - 1].rowid : null
  return { rows, next }
}

// ── share links, owner side — mirrors
// `crates/sc-http/src/core_api.rs::ShareLinkInfo`/`ShareLinkCreate`/
// `ShareLinkPatch` closely enough to drive the manage-links UI in dev mode:
// a link's `perms` defaults to read+download when the caller doesn't specify
// any (same default `sc-server/src/bridge.rs::share_link_create` applies),
// the plaintext token/url are only ever present on the create response, and
// a `PATCH` field left `undefined` is left alone while `null` clears it.

let mockShareLinks: ShareLinkInfo[] = []
let nextShareLinkId = 1

function fullPerms(p?: Partial<Entry['perms']>): Entry['perms'] {
  return {
    read: p?.read ?? false,
    write: p?.write ?? false,
    create: p?.create ?? false,
    delete: p?.delete ?? false,
    rename: p?.rename ?? false,
    move: p?.move ?? false,
    share: p?.share ?? false,
    download: p?.download ?? false
  }
}

async function sharesList(path?: string): Promise<ShareLinkInfo[]> {
  await delay(20)
  const scoped = path ? mockShareLinks.filter((l) => normalizePath(l.path) === normalizePath(path)) : mockShareLinks
  // Never echo the one-time `token` back on a list/get read — same rule the
  // real server follows (`ShareLinkInfo.token` only populated on create).
  return scoped.map(({ token: _token, url: _url, ...rest }) => ({ ...rest }))
}

async function shareCreate(req: ShareLinkCreateReq): Promise<ShareLinkInfo> {
  await delay(50)
  const n = normalizePath(req.path)
  const parent = parentOf(n)
  const name = baseName(n)
  const target = n === '/' ? null : resolveDirEntries(parent).find((e) => e.name === name)
  if (n !== '/' && !target) {
    throw new ApiError(404, { code: 'fs.not_found', message: 'not found', detail: { path: n } })
  }
  // The same refusal `sc-core::links::create_link` makes: a drop link has
  // nowhere to put an upload unless its target is a directory. Without this
  // the mock accepts a link the real server rejects, and the UI that offers
  // it only looks correct until it runs against a real backend.
  const isDrop = !!req.perms?.create && !req.perms?.read && !req.perms?.download
  if (isDrop && target && target.kind !== 'dir') {
    throw new ApiError(422, {
      code: 'fs.invalid_name',
      message: 'a file-drop link must target a directory',
      detail: { path: n }
    })
  }
  const link: ShareLinkInfo = {
    id: nextShareLinkId++,
    path: n,
    perms: req.perms ? fullPerms(req.perms) : fullPerms({ read: true, download: true }),
    expires_ns: req.expires_ns ?? null,
    max_downloads: req.max_downloads ?? null,
    downloads: 0,
    label: req.label ?? null,
    has_password: !!req.password,
    created_ns: String(BigInt(Date.now()) * 1_000_000n),
    token: randomId('tok'),
    url: `${typeof location !== 'undefined' ? location.origin : ''}/s/${randomId('tok')}`
  }
  mockShareLinks = [link, ...mockShareLinks]
  return link
}

async function shareUpdate(id: number, patch: ShareLinkPatchReq): Promise<ShareLinkInfo> {
  await delay(40)
  const link = mockShareLinks.find((l) => l.id === id)
  if (!link) throw new ApiError(404, { code: 'fs.not_found', message: 'not found' })
  const updated: ShareLinkInfo = {
    ...link,
    perms: patch.perms ? fullPerms({ ...link.perms, ...patch.perms }) : link.perms,
    expires_ns: 'expires_ns' in patch ? (patch.expires_ns ?? null) : link.expires_ns,
    max_downloads: 'max_downloads' in patch ? (patch.max_downloads ?? null) : link.max_downloads,
    label: 'label' in patch ? (patch.label ?? null) : link.label,
    has_password: 'password' in patch ? !!patch.password : link.has_password,
    token: undefined,
    url: undefined
  }
  mockShareLinks = mockShareLinks.map((l) => (l.id === id ? updated : l))
  const { token: _token, url: _url, ...rest } = updated
  return { ...rest }
}

async function shareDelete(id: number): Promise<void> {
  await delay(30)
  if (!mockShareLinks.some((l) => l.id === id)) {
    throw new ApiError(404, { code: 'fs.not_found', message: 'not found' })
  }
  mockShareLinks = mockShareLinks.filter((l) => l.id !== id)
}

export interface SearchHit {
  path: string
  entry: Entry
}

function searchStream(query: string, onHit: (hit: SearchHit) => void, onDone: () => void): () => void {
  let cancelled = false
  const q = query.trim().toLowerCase()

  async function run() {
    if (!q) {
      onDone()
      return
    }
    // Search small static dirs immediately.
    for (const dir of STATIC_SEED) {
      if (cancelled) return
      for (const e of resolveDirEntries(dir.path)) {
        if (e.name.toLowerCase().includes(q)) onHit({ path: joinPath(dir.path, e.name), entry: e })
      }
    }
    // Search /bench in yielding batches so we never block the main thread for long.
    const BATCH = 4000
    for (let i = 0; i < BENCH_COUNT; i += BATCH) {
      if (cancelled) return
      const end = Math.min(i + BATCH, BENCH_COUNT)
      for (let j = i; j < end; j++) {
        const e = benchEntryAt(j)
        if (isTombstoned(BENCH_DIR, e.name)) continue
        if (e.name.toLowerCase().includes(q)) onHit({ path: joinPath(BENCH_DIR, e.name), entry: e })
      }
      await delay(0)
    }
    if (!cancelled) onDone()
  }

  run()
  return () => {
    cancelled = true
  }
}

export const mockApi = {
  session,
  login,
  loginTotp,
  logout,
  list,
  stat,
  mkdir,
  rename,
  copy,
  move,
  movePreflight,
  delete: del,
  link,
  archive,
  jobList,
  jobStatus,
  jobCancel,
  jobDownload,
  trashList,
  trashRestore,
  trashPurge,
  sharesList,
  shareCreate,
  shareUpdate,
  shareDelete,
  readFile,
  writeFile,
  searchStream,
  changePassword,
  totpSetup,
  totpEnroll,
  totpDisable,
  recoveryCodesRemaining,
  reissueRecoveryCodes,
  listAppPasswords,
  createAppPassword,
  createScopedAppPassword,
  revokeAppPassword,
  listSessions,
  revokeSession,
  updateSmbSettings,
  oidcLinkStart,
  oidcUnlink,
  adminStorage,
  adminIndexEstimate,
  adminIndexSettings,
  adminSetIndexSettings,
  adminBuildIndex,
  adminSetUploadSettings,
  adminGetServerSettings,
  adminSetSmbSettings,
  adminSetSearchSettings,
  adminSetArchiveSettings,
  adminSetNetworkSettings,
  adminSetDbSettings,
  adminSetSymlinkPolicySettings,
  adminSetHomesSettings,
  adminSetWatchSettings,
  adminSetOidcSettings,
  adminSetPathsSettings,
  adminRestartServer,
  adminListUsers,
  adminCreateUser,
  adminSetUserDisabled,
  adminSetUserQuota,
  adminDeleteUser,
  adminGetUserOidc,
  adminLinkUserOidc,
  adminUnlinkUserOidc,
  adminListShares,
  adminCreateShare,
  adminUpdateShare,
  adminDeleteShare,
  adminListGrants,
  adminCreateGrant,
  adminUpdateGrant,
  adminDeleteGrant,
  adminListGroups,
  adminCreateGroup,
  adminRenameGroup,
  adminDeleteGroup,
  adminAddGroupMember,
  adminRemoveGroupMember,
  adminListAudit,
  /** Called by the upload worker (via the browse UI) once a mock upload finalizes. */
  registerUploadedEntry(destDir: string, entry: Entry): void {
    addOverlayEntry(destDir, entry)
  }
}

export type MockApi = typeof mockApi
