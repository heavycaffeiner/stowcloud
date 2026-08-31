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
  ALL_GRANT_PERMS,
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
  type ArchiveListing,
  type ArchiveSettingsReq,
  type RateSettingsReq,
  type AuditPage,
  type FolderSize,
  type RecentHit,
  type AuditQuery,
  type AuditRow,
  type BatchItemResult,
  type BatchResult,
  type CopyResult,
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
  type SettingsSectionId,
  type ShareLinkCreateReq,
  type ShareLinkInfo,
  type ShareLinkPatchReq,
  type SmbCredential,
  type SmbSettingsReq,
  type SmbUnavailableReason,
  type SortKey,
  type StorageReport,
  type SymlinkPolicyReq,
  type TrashEntry,
  type UpdateGrantReq,
  type UpdateGroupReq,
  type UpdateShareReq,
  type SMBOutcome,
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

// ── listings ──
//
// The server keeps no per-listing session: it re-walks the directory and cuts
// the slice each time, so the handle it hands back is the path itself and the
// cursor is a decimal offset into the sorted listing. This mirrors that rather
// than inventing a session store, because a mock that models a server that
// does not exist proves nothing about the one that does.

function sortedEntriesOf(path: string, sort: SortKey, order: Order): Entry[] {
  const entries = resolveDirEntries(normalizePath(path))
  entries.sort((a, b) => compareEntries(a, b, sort, order))
  return entries
}

export interface ListOpts {
  sort?: SortKey
  order?: Order
  /**
   * The page to fetch, taken from the previous page's `cursor`.
   *
   * Opaque, and only ever a value the server handed out. There is no offset
   * beside it: the server orders the whole directory and cuts the page this
   * names, so a caller cannot ask for a slice starting at an arbitrary row.
   */
  cursor?: string
  limit?: number
  /** Lets the caller cancel a windowed fetch for a range it has scrolled past. */
  signal?: AbortSignal
}

/**
 * A directory page, paged the way the server pages: a cursor walk.
 *
 * The cursor is an opaque index here, which is enough for a mock. What it is
 * not is a random-access offset the caller may compute: the real server does
 * not accept one, so accepting one here would let the app drift into using a
 * window the backend cannot serve.
 */
async function list(path: string, opts: ListOpts): Promise<ListResponse> {
  await delay(30, opts.signal)
  const limit = Math.min(opts.limit ?? 200, 2000)
  const sort = opts.sort ?? 'name'
  const order = opts.order ?? 'asc'

  const dir = normalizePath(path)
  const entries = sortedEntriesOf(dir, sort, order)
  const etag = dirEtagFor(dir)

  const offset = opts.cursor ? Number(opts.cursor) || 0 : 0
  const page = entries.slice(offset, offset + limit)
  const next = offset + page.length

  return {
    total: entries.length,
    dirs: entries.filter((e) => e.kind === 'dir').length,
    cursor: next < entries.length ? String(next) : null,
    entries: page,
    dir_etag: etag,
    dir_etag_weak: true
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
    path: n.replace(/^\//, ''),
    kind: 'dir',
    size: 0,
    mtime_ns: (BigInt(Date.now()) * 1_000_000n).toString(),
    etag: randomId('e'),
    etag_weak: true,
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
 *  source survives, so the conflict and rename-suffix behaviour
 *  they must agree on lives here once.
 *
 *  The suffix matches the server's (`(2)`, `(3)`, …, never `(1)`), and the
 *  result carries the name the item actually landed under: a rename that
 *  reported the requested path back would tell the caller a file exists under
 *  a name nothing wrote. */
async function transfer(req: MoveReq, keepSource: boolean): Promise<BatchResult> {
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
      if (destExists && req.on_conflict === 'fail') {
        throw new ApiError(409, { code: 'fs.conflict', message: 'destination already exists', detail: { path: joinPath(destDir, name) } })
      }
      if (destExists && req.on_conflict === 'skip') {
        results.push({ path: joinPath(destDir, name), ok: true, skipped: true })
        continue
      }
      let finalName = name
      if (destExists && req.on_conflict === 'rename') {
        const dot = name.lastIndexOf('.')
        const [stem, ext] = dot > 0 ? [name.slice(0, dot), name.slice(dot)] : [name, '']
        let i = 2
        while (resolveDirEntries(destDir).some((e) => e.name === finalName)) {
          finalName = `${stem} (${i++})${ext}`
        }
      }

      addOverlayEntry(destDir, { ...entry, name: finalName, etag: randomId('e') })
      if (!keepSource) removeEntry(parent, name)
      results.push({ path: joinPath(destDir, finalName), ok: true })
    } catch (err) {
      const e = err instanceof ApiError ? err : new ApiError(500, { code: 'internal', message: 'internal error' })
      results.push({ path: p, ok: false, error: { code: e.code, message: e.message, detail: e.detail } })
    }
  }
  return { results }
}

/** A copy is a durable job: it rewrites every byte whatever the two paths
 *  are, so the server answers with an id and the tray polls it.
 *
 *  The destination is checked before the job exists, so a conflict is in this
 *  response rather than in the job's own results, and a batch where nothing
 *  started carries no job at all. */
async function copy(req: MoveReq): Promise<CopyResult> {
  const { results } = await transfer(req, true)
  const started = results.filter((r) => r.ok && !r.skipped)
  if (started.length === 0) return { results }
  return { results, job: makeMockJob('copy', started.length, started) }
}

/** A move finishes in the request: it is a rename. */
async function move(req: MoveReq): Promise<BatchResult> {
  return transfer(req, false)
}

/** The mock tree is one device, so a move here is always a rename and never
 *  the copy-then-delete fallback the real server warns about. */
async function movePreflight(req: MoveReq): Promise<MovePreflight> {
  await delay(10)
  return { results: req.paths.map((p) => ({ path: normalizePath(p), ok: true })) }
}

async function del(paths: string[], permanent = false): Promise<{ results: BatchItemResult[] }> {
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
  // One result per path, which is what the server answers. This used to wrap
  // them in a job id, and the caller then polled a job the real server never
  // created.
  return { results }
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
}

const mockJobs = new Map<string, MockJobRow>()

function makeMockJob(kind: JobKindWire, total: number, results: BatchItemResult[]): string {
  const id = randomId('job')
  // A single failed item ends the whole job in `error` on the real server
  // (`go/internal/httpapi/handler/ops.go`). This
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
      pending: []
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

// ── trash (mirrors `go/internal/core/trash` closely enough to drive the
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
    .map((row) => ({ id: row.id, name: row.entry.name, size: row.entry.size, deleted_at_ns: row.deletedNs }))
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

/** Mock counterpart of `http.ts`'s `archive`: the real server streams the zip
 *  as the response body, so this hands back a blob rather than a job id. */
async function archive(paths: string[], _name?: string): Promise<Blob> {
  await delay(120)
  if (paths.length === 0) throw new ApiError(422, { code: 'fs.invalid_name', message: 'paths must not be empty' })
  const body = `mock zip archive of:\n${paths.join('\n')}\n`
  return new Blob([body], { type: 'application/zip' })
}

/** A fixed listing for any `.zip`, since the mock stores no archive bytes. */
async function archiveList(path: string): Promise<ArchiveListing> {
  await delay(60)
  const n = normalizePath(path)
  const e = entryAt(n)
  if (!e || e.kind === 'dir' || !n.toLowerCase().endsWith('.zip')) {
    throw new ApiError(404, { code: 'fs.not_found', message: 'not found', detail: { path: n } })
  }
  // No size ceiling any more: a listing reads the central directory, so what
  // an archive costs follows its entry count and not its bytes. The rejection
  // that remains is the entry-count cap, which this tree never reaches.
  return {
    truncated: false,
    limit: 10_000,
    entries: [
      { name: 'docs/', size: 0, kind: 'dir' },
      { name: 'docs/readme.txt', size: 1_842, kind: 'file' },
      { name: 'docs/design.pdf', size: 902_144, kind: 'file' },
      { name: 'docs/notes/', size: 0, kind: 'dir' },
      { name: 'docs/notes/todo.md', size: 512, kind: 'file' },
      { name: 'images/', size: 0, kind: 'dir' },
      { name: 'images/cover.png', size: 3_211_776, kind: 'file' }
    ],
    skipped: 0
  }
}

/** Walks the seeded tree, so the number the panel shows is the sum of what the
 *  same tree lists. */
/** No decoder in the mock, so every card falls back to its type icon. A data
 *  URL of a fake image would make the grid look right and prove nothing. */
function thumbUrl(_path: string, _dim: number): string {
  return ''
}

async function folderSize(path: string): Promise<FolderSize> {
  await delay(200)
  const n = normalizePath(path)
  const e = entryAt(n)
  if (n !== '/' && (!e || e.kind !== 'dir')) {
    throw new ApiError(404, { code: 'fs.not_found', message: 'not found', detail: { path: n } })
  }
  let bytes = 0
  let files = 0
  const walk = (dir: string): void => {
    // The 100k-row benchmark directory is generated on demand and is not what
    // this control is for; walking it here would block the main thread for
    // seconds in mock mode alone.
    if (dir === BENCH_DIR) return
    for (const child of resolveDirEntries(dir)) {
      if (child.kind === 'dir') {
        walk(joinPath(dir, child.name))
      } else {
        bytes += child.size
        files += 1
      }
    }
  }
  walk(n)
  return { bytes, files }
}

/** Every file in the seeded tree, newest first, as though this account had
 *  written all of them. `since_days` is honoured so the tab's own controls do
 *  something; `scope` narrows it. */
async function recentList(
  opts: { limit?: number; sinceDays?: number; scope?: string } = {}
): Promise<{ hits: RecentHit[] }> {
  await delay(120)
  const limit = Math.min(Math.max(opts.limit ?? 100, 1), 500)
  const sinceDays = Math.min(Math.max(opts.sinceDays ?? 30, 1), 365)
  const cutoff = BigInt(Date.now() - sinceDays * 86_400_000) * 1_000_000n
  const root = opts.scope ? normalizePath(opts.scope) : '/'
  const hits: RecentHit[] = []
  const walk = (dir: string): void => {
    if (dir === BENCH_DIR) return
    for (const child of resolveDirEntries(dir)) {
      const full = joinPath(dir, child.name)
      if (child.kind === 'dir') {
        walk(full)
      } else if (BigInt(child.mtime_ns) >= cutoff) {
        const vpath = full.replace(/^\//, '')
        const label = vpath.split('/')[0] ?? ''
        hits.push({
          vpath,
          share: label,
          // Sent explicitly, as the server does, rather than left for the
          // caller to cut back out of the path.
          subpath: vpath.slice(label.length + 1),
          name: child.name,
          size: child.size,
          mtime_ns: child.mtime_ns,
          at_ns: child.mtime_ns,
          op: 'upload'
        })
      }
    }
  }
  walk(root)
  hits.sort((a, b) => {
    if (a.mtime_ns === b.mtime_ns) return a.vpath.localeCompare(b.vpath)
    return BigInt(a.mtime_ns) < BigInt(b.mtime_ns) ? 1 : -1
  })
  return { hits: hits.slice(0, limit) }
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

/** Mirrors `go/internal/core/ops.go`: an existing file demands
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
        path: n.replace(/^\//, ''),
        kind: 'file',
        size: content.length,
        mtime_ns: nowNs,
        etag: randomId('e'),
        etag_weak: true,
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
  /** Whether a separate SMB-only password has been set. The credential itself
   *  is never modelled: the mock stores no NT hash and never could. */
  smbDedicated: boolean
} = {
  loggedIn: true,
  pendingChallenge: null,
  password: DEMO_USER.password,
  totpEnabled: false,
  smbOptOut: false,
  smbEnabled: true,
  smbDedicated: false,
  pendingTotpSecret: null,
  recoveryCodesRemaining: 0,
  chunkMin: 5 * 1024 * 1024,
  chunkDefault: 10 * 1024 * 1024,
  indexNameEnabled: false,
  oidcLinked: true
}

/**
 * What works over SMB right now, folded the same way the server folds it. The
 * mock deployment's TOTP policy is the default (`require_separate`), so a
 * TOTP account is never `totp_blocked` here.
 */
function mockSmbCredential(): {
  smb_credential: SmbCredential
  smb_unavailable_reason?: SmbUnavailableReason
} {
  if (mockAuthState.smbOptOut || !mockAuthState.smbEnabled) {
    return { smb_credential: 'none', smb_unavailable_reason: 'opted_out' }
  }
  if (mockAuthState.smbDedicated) return { smb_credential: 'dedicated' }
  if (mockAuthState.totpEnabled || mockAuthState.oidcLinked) {
    return { smb_credential: 'none', smb_unavailable_reason: 'not_set' }
  }
  return { smb_credential: 'account' }
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
      smb_enabled: mockAuthState.smbEnabled,
      ...mockSmbCredential()
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

async function oidcUnlink(password: string): Promise<{ smb_password_replaced: boolean }> {
  await delay(80)
  if (password !== mockAuthState.password) {
    throw new ApiError(401, { code: 'auth.invalid_credentials', message: 'invalid credentials' })
  }
  if (!mockAuthState.oidcLinked) {
    throw new ApiError(404, { code: 'oidc.not_linked', message: 'no linked identity' })
  }
  mockAuthState.oidcLinked = false
  // Identical rule to `totpDisable`, on purpose: a user who has been through
  // one of them should not have to learn a second rule for the other.
  const replaced = mockAuthState.smbDedicated && !mockAuthState.totpEnabled && !mockAuthState.smbOptOut
  if (replaced) mockAuthState.smbDedicated = false
  return { smb_password_replaced: replaced }
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

// Scoped against unscoped, used against never used, expiring against not: the
// read-only chip and the two date lines each need a row that shows them and a
// row beside them that does not.
const pwTs = (days: number) => String(BigInt(Date.now() - days * 86_400_000) * 1_000_000n)

let mockAppPasswords: AppPasswordInfo[] = [
  { id: 1, name: '노트북 동기화', created_ns: pwTs(90), last_used_ns: pwTs(1), expires_ns: null },
  { id: 2, name: '백업 스크립트 (읽기 전용)', created_ns: pwTs(45), last_used_ns: pwTs(7), expires_ns: null, read_only: true },
  { id: 3, name: 'iPhone', created_ns: pwTs(3), last_used_ns: null, expires_ns: pwTs(-60) }
]
let nextAppPasswordId = 4

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
  },
  // The current-session badge only means something next to a row without it,
  // and a real user-agent string is what tells the row whether it can wrap.
  {
    id_hash: 'mock-session-phone',
    created_ns: String(BigInt(Date.now() - 4 * 86_400_000) * 1_000_000n),
    last_seen_ns: String(BigInt(Date.now() - 3_600_000) * 1_000_000n),
    absolute_expiry_ns: String(BigInt(Date.now() + 26 * 24 * 3600 * 1000) * 1_000_000n),
    ip_first: '192.0.2.44',
    ua_first: 'Mozilla/5.0 (iPhone; CPU iPhone OS 18_2 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.2 Mobile/15E148 Safari/604.1',
    current: false
  },
  {
    id_hash: 'mock-session-desktop',
    created_ns: String(BigInt(Date.now() - 20 * 86_400_000) * 1_000_000n),
    last_seen_ns: String(BigInt(Date.now() - 9 * 86_400_000) * 1_000_000n),
    absolute_expiry_ns: String(BigInt(Date.now() + 10 * 24 * 3600 * 1000) * 1_000_000n),
    ip_first: '203.0.113.7',
    ua_first: 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36',
    current: false
  }
]

// No `revokeOtherSessions`: the real route does not honour one, so a mock
// that swept sessions here would demonstrate behaviour the server does not
// have.
async function changePassword(currentPassword: string, newPassword: string): Promise<void> {
  await delay(80)
  if (currentPassword !== mockAuthState.password) {
    throw new ApiError(401, { code: 'auth.invalid_credentials', message: 'invalid credentials' })
  }
  if (newPassword.length < 10) {
    throw new ApiError(422, {
      code: 'auth.weak_password',
      message: 'password is too short',
      detail: { reason_key: 'auth.weak_password', reason_params: { min_length: '10' } }
    })
  }
  mockAuthState.password = newPassword
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

async function totpDisable(password: string): Promise<{ smb_password_replaced: boolean }> {
  await delay(80)
  if (password !== mockAuthState.password) {
    throw new ApiError(401, { code: 'auth.invalid_credentials', message: 'invalid credentials' })
  }
  mockAuthState.totpEnabled = false
  mockAuthState.recoveryCodesRemaining = 0
  // The undo of what made a separate SMB password necessary, so it goes back
  // to the account password.
  const replaced = mockAuthState.smbDedicated && !mockAuthState.oidcLinked && !mockAuthState.smbOptOut
  if (replaced) mockAuthState.smbDedicated = false
  return { smb_password_replaced: replaced }
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

/**
 * Mark the device holding this app password as lost.
 *
 * Deliberately not a revoke, exactly as `http.ts` documents: the credential
 * keeps working until the device reports it has erased its local copies, which
 * it can only do while it can still authenticate. Nothing in the mock models a
 * device reporting back, so the row stays as it is and this only proves the
 * call exists.
 *
 * It did not exist, and `api` is `mockApi | httpApi`, so the settings screen's
 * "mark as lost" button called a method that is not on the union: a type error
 * that nothing read until `svelte-check` became a CI gate, and a runtime
 * `is not a function` in mock mode before that.
 */
async function wipeAppPassword(id: number): Promise<void> {
  await delay(30)
  if (!mockAppPasswords.some((p) => p.id === id)) {
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

async function setSmbPassword(
  currentPassword: string,
  smbPassword: string
): Promise<{ smb_toggles_cleared: boolean }> {
  await delay(60)
  if (currentPassword !== mockAuthState.password) {
    throw new ApiError(401, { code: 'auth.invalid_credentials', message: 'invalid credentials' })
  }
  if (smbPassword.length < 10) {
    throw new ApiError(422, {
      code: 'auth.weak_password',
      message: 'password is too short',
      detail: { reason_key: 'auth.weak_password', reason_params: { min_length: '10' } }
    })
  }
  const cleared = mockAuthState.smbOptOut || !mockAuthState.smbEnabled
  mockAuthState.smbOptOut = false
  mockAuthState.smbEnabled = true
  mockAuthState.smbDedicated = true
  return { smb_toggles_cleared: cleared }
}

async function clearSmbPassword(
  currentPassword: string
): Promise<{ reverted_to_account_password: boolean }> {
  await delay(60)
  if (currentPassword !== mockAuthState.password) {
    throw new ApiError(401, { code: 'auth.invalid_credentials', message: 'invalid credentials' })
  }
  if (!mockAuthState.smbDedicated) {
    throw new ApiError(404, { code: 'fs.not_found', message: 'not found' })
  }
  mockAuthState.smbDedicated = false
  return {
    reverted_to_account_password:
      !mockAuthState.totpEnabled && !mockAuthState.oidcLinked && !mockAuthState.smbOptOut
  }
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
  if (req.cache_enabled !== undefined) {
    mockUploadCacheEnabled = req.cache_enabled
  }
  return {
    chunk_min: req.chunk_min,
    chunk_default: req.chunk_default,
    cache_enabled: mockUploadCacheEnabled,
    cache_available: true
  }
}

/** The cache spool switch, which the real server keeps in `upload_cache_settings`. */
let mockUploadCacheEnabled = false

// ── server settings (`go/internal/httpapi/handler/settings.go`) — mirrors
// `http.ts`'s real-server surface, same convention as every other section of
// this file. Field keys match `go/internal/runtimecfg`'s dotted paths
// exactly, since `ServerSettingsSection.svelte` groups by those literal
// strings regardless of which backend answered them. ──

const mockServerSettings = {
  smb: {
    enabled: false,
    workgroup: 'WORKGROUP',
    server_name: 'NAS',
    service_user: 'sc-smb',
    allow_public_bind: false,
    totp_policy: 'require_separate' as 'require_separate' | 'block',
    service_gid: 1000
  },
  search: {
    max_concurrent_fast: 4,
    max_concurrent_slow: 2,
    walk_deadline_fast_ms: 100,
    walk_deadline_slow_ms: 500
  },
  archive: { max_concurrent: 2 },
  rate: { per_sec: 20, burst: 40 },
  network: {
    app_hosts: ['nas.local'] as string[],
    trusted_proxies: [] as string[]
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
    redirect_uris: [] as string[],
    scopes: ['openid', 'profile'] as string[],
    display_name: '',
    allow_private_endpoints: false,
    smb_policy: 'block' as const
  }
}

/** The groups whose stored override the mock is currently holding, so the
 *  revert button behaves the way it does against a real server: enabled only
 *  where something is overriding the file, and disabled again after a revert. */
const mockOverriddenSections = new Set<SettingsSectionId>(['network', 'smb'])

/** What each group falls back to when its override is dropped, standing in
 *  for the stored settings plus the environment. */
const mockFileSettings = JSON.parse(JSON.stringify(mockServerSettings)) as typeof mockServerSettings
mockFileSettings.network.app_hosts = ['localhost']
mockFileSettings.smb.server_name = 'STOWCLOUD'

/** Which group each row belongs to, so the screen can ask one field for a
 *  group's source. */
const MOCK_SECTION_OF: Record<string, SettingsSectionId> = {
  app_hosts: 'network',
  trusted_proxies: 'network',
  symlink_policy: 'symlink-policy',
  data_dir: 'paths',
  master_key_file: 'paths',
  'smb.config_dir': 'paths'
}

function sectionOf(key: string): SettingsSectionId | undefined {
  if (MOCK_SECTION_OF[key]) return MOCK_SECTION_OF[key]
  const prefix = key.split('.')[0]
  if (['db', 'homes', 'smb', 'search', 'archive', 'watch', 'oidc'].includes(prefix)) {
    return prefix as SettingsSectionId
  }
  return undefined
}

function settingsField(
  key: string,
  value: unknown,
  restartRequired: boolean,
  readonlyReasonKey: string | null = null
): SettingsField {
  const section = sectionOf(key)
  return {
    key,
    value,
    source: section && mockOverriddenSections.has(section) ? 'admin_override' : 'builtin_default',
    restart_required: restartRequired,
    readonly_reason_key: readonlyReasonKey
  }
}

async function adminGetServerSettings(): Promise<SettingsSnapshot> {
  await delay(20)
  const s = mockServerSettings
  return {
    fields: [
      // The listener's address and the data directory: reported, never
      // editable. The socket is bound once at startup and everything open was
      // opened relative to the directory, which is what the server says about
      // both.
      settingsField('bind', mockBind, false),
      settingsField('data_dir', '/var/lib/stowcloud', false, /* i18n */ 'settings.readonly_data_dir'),
      settingsField('app_hosts', s.network.app_hosts, false),
      settingsField('trusted_proxies', s.network.trusted_proxies, false),
      settingsField('db.size_guard', s.db.size_guard, true),
      settingsField('db.max_bytes', s.db.max_bytes, true),
      settingsField('db.min_free_bytes', s.db.min_free_bytes, true),
      settingsField('symlink_policy', s.symlink_policy, true),
      settingsField('homes.enabled', s.homes.enabled, true),
      settingsField('homes.root', s.homes.root, true),
      settingsField('smb.enabled', s.smb.enabled, true),
      settingsField('smb.workgroup', s.smb.workgroup, false),
      settingsField('smb.server_name', s.smb.server_name, false),
      settingsField('smb.service_user', s.smb.service_user, false),
      settingsField('smb.allow_public_bind', s.smb.allow_public_bind, false),
      settingsField('smb.totp_policy', s.smb.totp_policy, false),
      settingsField('smb.service_gid', s.smb.service_gid, false),
      settingsField('search.max_concurrent_fast', s.search.max_concurrent_fast, false),
      settingsField('search.max_concurrent_slow', s.search.max_concurrent_slow, false),
      settingsField('search.walk_deadline_fast_ms', s.search.walk_deadline_fast_ms, false),
      settingsField('search.walk_deadline_slow_ms', s.search.walk_deadline_slow_ms, false),
      settingsField('archive.max_concurrent', s.archive.max_concurrent, false),
      settingsField('rate.per_sec', s.rate.per_sec, false),
      settingsField('rate.burst', s.rate.burst, false),
      settingsField('watch.backend', s.watch.backend, true),
      settingsField('watch.hot_set_max', s.watch.hot_set_max, true),
      settingsField('watch.full_threshold', s.watch.full_threshold, true),
      settingsField('data_dir', s.paths.data_dir, true),
      settingsField('master_key_file', s.paths.master_key_file, true),
      settingsField('smb.config_dir', s.paths.smb_config_dir, true),
      settingsField('oidc.enabled', s.oidc.enabled, true),
      settingsField('oidc.issuer', s.oidc.issuer, true),
      settingsField('oidc.client_id', s.oidc.client_id, true),
      settingsField('oidc.redirect_uris', s.oidc.redirect_uris, true),
      settingsField('oidc.scopes', s.oidc.scopes, true),
      settingsField('oidc.display_name', s.oidc.display_name, true),
      settingsField('oidc.allow_private_endpoints', s.oidc.allow_private_endpoints, true),
      settingsField('oidc.smb_policy', s.oidc.smb_policy, true),
      // The two the process settles before anything is configurable. They carry their reason
      // here the same way the real bridge writes it, so the screen's
      // read-only list is not empty under the mock.
      {
        key: 'oidc.client_secret_file',
        value: null,
        source: 'builtin_default',
        restart_required: true,
        readonly_reason_key: 'settings.readonly_secret_file_path'
      },
      {
        key: 'oidc.local_password_login',
        value: 'allow',
        source: 'builtin_default',
        restart_required: true,
        readonly_reason_key: 'settings.readonly_local_password_login'
      },
      // Owned by other admin screens, and reported live by the real bridge.
      {
        key: 'index.name_enabled',
        value: false,
        source: 'builtin_default',
        restart_required: false,
        readonly_reason_key: 'settings.readonly_owned_by_index_section'
      },
      {
        key: 'upload.chunk_min_bytes',
        value: 5 * 1024 * 1024,
        source: 'builtin_default',
        restart_required: false,
        readonly_reason_key: 'settings.readonly_owned_by_upload_section'
      },
      {
        key: 'upload.chunk_default_bytes',
        value: 10 * 1024 * 1024,
        source: 'builtin_default',
        restart_required: false,
        readonly_reason_key: 'settings.readonly_owned_by_upload_section'
      }
    ],
    smb_public_bind_warning: false,
    smb_overgrants: [],
    // The ordinary case: the agent answered and everything it checked was
    // fine. The interesting shapes to try by hand are a non-empty
    // `missing_paths` (a share mounted on this side and not on the file
    // server's) and `key: 'smb.agent_unreachable'`.
    smb_agent: {
      key: 'smb.agent_applied',
      ok: true,
      shares: ['Share'],
      interfaces: 'lo eth0',
      hosts_allow: '127.0.0.0/8 ::1/128 192.168.0.0/16',
      smbd: 'reloaded',
      missing_paths: [],
      missing_passdb: [],
      detail: null
    }
  }
}

async function adminSetSmbSettings(req: SmbSettingsReq): Promise<ApplyOutcome> {
  await delay(30)
  const enabledChanged = mockServerSettings.smb.enabled !== req.enabled
  mockServerSettings.smb = { ...mockServerSettings.smb, ...req }
  mockOverriddenSections.add('smb')
  return { applied: enabledChanged ? 'engine_restart' : 'live' }
}

/** The groups this process cannot move while running, matching the server. */
const RESTART_REQUIRED_SECTIONS = new Set<SettingsSectionId>([
  'db',
  'paths',
  'homes',
  'smb',
  'watch',
  'symlink-policy',
  'oidc'
])

/** Zero rejects every search from every user the moment it is applied, so the
 *  real bridge refuses it where it is typed and so does this. */
async function adminSetSearchSettings(req: SearchSettingsReq): Promise<ApplyOutcome> {
  await delay(30)
  mockServerSettings.search = { ...req }
  mockOverriddenSections.add('search')
  return { applied: 'live' }
}

function mustBeAtLeastOne(field: string): ApiError {
  return new ApiError(422, {
    code: 'fs.invalid_name',
    message: 'invalid name',
    detail: {
      reason: `${field}: must be at least 1`,
      reason_key: 'settings.must_be_at_least_one',
      reason_params: { field }
    }
  })
}

async function adminSetArchiveSettings(req: ArchiveSettingsReq): Promise<ApplyOutcome> {
  await delay(30)
  if (req.max_concurrent < 1) throw mustBeAtLeastOne('archive.max_concurrent')
  mockServerSettings.archive = { ...req }
  mockOverriddenSections.add('archive')
  return { applied: 'live' }
}

async function adminSetRateSettings(req: RateSettingsReq): Promise<ApplyOutcome> {
  await delay(30)
  if (req.per_sec < 1) throw mustBeAtLeastOne('rate.per_sec')
  if (req.burst < 1) throw mustBeAtLeastOne('rate.burst')
  mockServerSettings.rate = { ...req }
  mockOverriddenSections.add('rate')
  return { applied: 'live' }
}

let mockBind = '0.0.0.0:8443'

async function adminSetNetworkSettings(req: NetworkSettingsReq): Promise<ApplyOutcome> {
  await delay(30)
  const badCidr = req.trusted_proxies.find((c) => !/^[0-9a-fA-F.:]+\/\d{1,3}$/.test(c.trim()))
  if (badCidr !== undefined) {
    throw new ApiError(422, {
      code: 'fs.invalid_request',
      message: 'invalid request',
      detail: { reason_key: 'settings.invalid_cidr', reason_params: { field: 'trusted_proxies' } }
    })
  }
  mockServerSettings.network = { ...req }
  mockOverriddenSections.add('network')
  // The listener moves when the address does: the server binds the new socket
  // before dropping the old one, so this is live rather than a restart.
  if (req.bind && req.bind !== mockBind) {
    mockBind = req.bind
    return { applied: 'serve_restarted' }
  }
  // Both fields are live holders the request chain reads, so a save moves
  // them at once. It answered restart-required, which is what the three
  // fields this request no longer carries would have needed.
  return { applied: 'live' }
}

async function adminSetDbSettings(req: DbSettingsReq): Promise<ApplyOutcome> {
  await delay(30)
  mockServerSettings.db = { ...req }
  mockOverriddenSections.add('db')
  return { applied: 'engine_restart' }
}

async function adminSetSymlinkPolicySettings(req: SymlinkPolicyReq): Promise<ApplyOutcome> {
  await delay(30)
  mockServerSettings.symlink_policy = req.policy
  mockOverriddenSections.add('symlink-policy')
  return { applied: 'engine_restart' }
}

async function adminSetHomesSettings(req: HomesSettingsReq): Promise<ApplyOutcome> {
  await delay(30)
  mockServerSettings.homes = { ...req }
  mockOverriddenSections.add('homes')
  return { applied: 'engine_restart' }
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
  mockOverriddenSections.add('watch')
  return { applied: 'engine_restart' }
}

/** Reproduces the two refusals a browser could not have made for itself: a
 *  redirect URI naming a host this deployment does not answer for, and a
 *  client secret file only the server can try to read. Both keep OIDC
 *  switched off with everything else still running, so a screen that only
 *  found out at the next boot would be a screen that lies. */
async function adminSetOidcSettings(req: OidcSettingsReq): Promise<ApplyOutcome> {
  await delay(30)
  if (req.enabled) {
    const notHttps = req.redirect_uris.find((u) => !u.trim().startsWith('https://'))
    if (notHttps !== undefined) {
      throw new ApiError(422, {
        code: 'fs.invalid_name',
        message: 'invalid name',
        detail: {
          reason: 'oidc.redirect_uris: must start with https://',
          reason_key: 'settings.oidc_redirect_uri_must_be_https',
          reason_params: { value: notHttps }
        }
      })
    }
    const hosts = mockServerSettings.network.app_hosts
    const unserved = req.redirect_uris.find(
      (u) => !hosts.some((h) => u.trim().slice('https://'.length).split(/[/:?#]/)[0].toLowerCase() === h.toLowerCase())
    )
    if (unserved !== undefined) {
      throw new ApiError(422, {
        code: 'fs.invalid_name',
        message: 'invalid name',
        detail: {
          reason: 'oidc.redirect_uris: names a host app_hosts does not admit',
          reason_key: 'settings.oidc_redirect_host_not_served',
          reason_params: { value: unserved }
        }
      })
    }
    // The mock has no filesystem, so it stands in for "no readable secret
    // file" with the one case it can represent: nothing configured at all.
    throw new ApiError(422, {
      code: 'fs.invalid_name',
      message: 'invalid name',
      detail: {
        reason: 'oidc.client_secret_file is not set or cannot be read',
        reason_key: 'settings.oidc_secret_file_missing',
        reason_params: { path: '' }
      }
    })
  }
  mockServerSettings.oidc = { ...req, smb_policy: 'block' }
  mockOverriddenSections.add('oidc')
  return { applied: 'engine_restart' }
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
  mockOverriddenSections.add('paths')
  return { applied: 'engine_restart' }
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
// be neither disabled nor deleted (`go/internal/auth/admin.go`).

// Six accounts, not one, and deliberately no two alike. Every conditional the
// row renders -- the administrator chip, the inactive chip, a quota against no
// quota, a long display name against none at all -- needs a row that shows it
// and a row beside it that does not, or the design gate has nothing to compare
// and the screen goes unaudited. One account cannot misalign with anything.
const seedUser = (
  id: number,
  name: string,
  display_name: string,
  extra: Partial<AdminUser> = {}
): AdminUser => ({
  id,
  name,
  display_name,
  is_admin: false,
  disabled: false,
  totp_enabled: false,
  smb_enabled: true,
  created_ns: String(BigInt(Date.now() - id * 86_400_000) * 1_000_000n),
  quota_bytes: null,
  usage_bytes: '0',
  ...extra
})

// Exactly one active administrator, and it stays the bootstrapped account:
// that is the invariant `mock.test.ts` checks, and the only way the
// last-active-admin guard has anything to refuse. The variety the design gate
// needs comes from the other five, none of which repeats a name the tests
// create.
let mockUsers: AdminUser[] = [
  seedUser(1, 'demo', '데모 사용자', { is_admin: true, usage_bytes: '0' }),
  seedUser(2, 'sujin', '김수진', { quota_bytes: '10737418240', usage_bytes: '3221225472' }),
  seedUser(3, 'minjun', '박민준', { totp_enabled: true, usage_bytes: '104857600' }),
  seedUser(4, 'seoyeon', '이서연 (프로젝트 관리)', { quota_bytes: '53687091200', usage_bytes: '48318382080' }),
  seedUser(5, 'contractor', '', { disabled: true, smb_enabled: false }),
  seedUser(6, 'backup-svc', '백업 서비스 계정', { disabled: true, quota_bytes: '1073741824', usage_bytes: '1073741824' })
]
let nextUserId = 7

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
    throw new ApiError(422, {
      code: 'auth.weak_password',
      message: 'password is too short',
      detail: { reason_key: 'auth.weak_password', reason_params: { min_length: '10' } }
    })
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
    throw new ApiError(409, {
      code: 'admin.last_admin',
      message: 'refusing to remove the last administrator',
      detail: { reason_key: 'admin.last_admin', reason_params: {} }
    })
  }
  user.disabled = disabled
  mockUsers = mockUsers.map((u) => (u.id === id ? { ...user } : u))
  return { ...user }
}

/** An administrator resetting an account they hold no current password for.
 *  The floor is the server's own, so the mock refuses what the server would. */
async function adminSetUserPassword(id: number, password: string): Promise<AdminUser> {
  await delay(40)
  const user = mockUsers.find((u) => u.id === id)
  if (!user) throw new ApiError(404, { code: 'fs.not_found', message: 'not found' })
  if (password.length < 10) {
    throw new ApiError(422, {
      code: 'auth.weak_password',
      message: 'password is too short',
      detail: { reason_key: 'auth.weak_password', reason_params: { min_length: '10' } }
    })
  }
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
    throw new ApiError(422, {
      code: 'admin.invalid_quota',
      message: 'quota must be greater than zero, or absent for unlimited',
      detail: { reason_key: 'admin.invalid_quota', reason_params: {} }
    })
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
    throw new ApiError(409, {
      code: 'admin.last_admin',
      message: 'refusing to remove the last administrator',
      detail: { reason_key: 'admin.last_admin', reason_params: {} }
    })
  }
  mockUsers = mockUsers.filter((u) => u.id !== id)
}

// ── admin: share and grant management (`GET/POST /api/admin/shares`,
// `PATCH/DELETE /api/admin/shares/{id}`, `GET/POST /api/admin/grants`,
// `PATCH/DELETE /api/admin/grants/{id}`) ── Mirrors `go/internal/acl` closely enough to drive the admin UI in dev mode:
// no-access-by-default, a grant needs at least one `allow` or `deny` bit,
// `subpath`/`share`/`principal` are immutable once created (delete and
// recreate instead — same rule `go/internal/acl` enforces
// server-side).
// The real endpoints now exist
// (`go/internal/httpapi/handler/shares.go` (
// `admin_update_share`/`admin_delete_share`/`admin_list_grants`/
// `admin_create_grant`/`admin_update_grant`/`admin_delete_grant`); this mock
// stays in sync with their wire shapes and error codes so
// `ShareManagementSection.svelte`/`GrantManagementSection.svelte` behave
// identically against either backend.

// The mock backend models one flat virtual tree (`STATIC_SEED`'s `/` listing),
// not the real server's per-share roots — there is no mock equivalent of
// `go/internal/core` to derive this from. A small fixed list
// matching the top-level folders `STATIC_SEED` already seeds is enough to
// demo the grant-creation screen's share picker.
// There is one kind of share: every one was created from this screen, because
// nothing else can declare one.
/** A share plus the host path the mock keeps but never answers with.
 *
 *  The server holds the same rule: where a share lives on its disk decides
 *  overlap at creation and is otherwise configuration a client never sees. */
type MockShare = AdminShare & { readonly hostPath: string }

let mockShares: MockShare[] = [
  { id: 1_000_001, name: 'Documents', hostPath: '/srv/documents', trash_enabled: false },
  { id: 1_000_002, name: 'Photos', hostPath: '/srv/photos', trash_enabled: true },
  { id: 1_000_003, name: 'Videos', hostPath: '/srv/videos', trash_enabled: false },
  { id: 1_000_004, name: 'Music', hostPath: '/srv/music', trash_enabled: false },
  { id: 1_000_005, name: 'Team', hostPath: '/srv/team', trash_enabled: false },
  { id: 1_000_006, name: 'Archive', hostPath: '/srv/archive', trash_enabled: true }
]

/** Drops the host path, which is what the server sends. */
function shareOf(s: MockShare): AdminShare {
  return { id: s.id, name: s.name, trash_enabled: s.trash_enabled, broken_reason: s.broken_reason }
}
let nextShareId = 1_000_007 // mirrors `go/internal/core`'s share id base, past the seeded ones

// One grant of each shape the row can take: inherited against path-only, a
// group principal against a user one, a long label against a bare share name.
const grantTs = (days: number) => String(BigInt(Date.now() - days * 86_400_000) * 1_000_000n)

let mockGrants: AdminGrant[] = [
  { id: 1, principal: { kind: 'user', id: 2 }, share: 1, subpath: '', allow: ['read', 'download'], deny: [], inherit: true, label: null, created_ns: grantTs(30) },
  { id: 2, principal: { kind: 'user', id: 3 }, share: 2, subpath: '2026', allow: ['read', 'write', 'create'], deny: ['delete'], inherit: false, label: null, created_ns: grantTs(12) },
  { id: 3, principal: { kind: 'group', id: 1 }, share: 1_000_001, subpath: '', allow: ALL_GRANT_PERMS, deny: [], inherit: true, label: '팀 공용 폴더 (전체 권한)', created_ns: grantTs(5) },
  { id: 4, principal: { kind: 'group', id: 2 }, share: 1_000_002, subpath: '2025/분기보고', allow: ['read'], deny: [], inherit: false, label: null, created_ns: grantTs(2) }
]
let nextGrantId = 5

async function adminListShares(): Promise<AdminShare[]> {
  await delay(15)
  return mockShares.map(shareOf)
}

/** No real filesystem to check `host_path` against in mock mode, so this
 *  only reproduces the checks that don't need one: empty/duplicate name and
 *  an overlapping `host_path` — the real backend's nonexistent-path/
 *  not-a-directory/unreadable checks (`go/internal/core/root.go`)
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
  if (mockShares.some((s) => s.hostPath === req.host_path)) {
    throw new ApiError(422, { code: 'fs.invalid_name', message: `overlaps existing share` })
  }
  const share: MockShare = { id: nextShareId++, name, hostPath: req.host_path, trash_enabled: false }
  mockShares = [...mockShares, share]
  return shareOf(share)
}

/** An edit is stored on the share's own row and reapplied at startup, so
 *  nothing is lost on restart (`go/internal/core`). */
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
  const updated: MockShare = {
    ...share,
    name: nextName,
    hostPath: patch.host_path ?? share.hostPath,
    trash_enabled: patch.trash_enabled ?? share.trash_enabled
  }
  mockShares = mockShares.map((s) => (s.id === id ? updated : s))
  return shareOf(updated)
}

async function adminDeleteShare(id: number): Promise<{ smb?: SMBOutcome }> {
  await delay(30)
  const share = mockShares.find((s) => s.id === id)
  if (!share) {
    throw new ApiError(404, { code: 'fs.not_found', message: 'not found' })
  }
  mockShares = mockShares.filter((s) => s.id !== id)
  mockGrants = mockGrants.filter((g) => g.share !== id)
  return {}
}

/** Re-opening a share whose disk came back. The mock has no filesystem, so
 *  the retry always succeeds: what it exercises is the screen's own path from
 *  broken back to working. */
async function adminRetryShare(id: number): Promise<AdminShare> {
  await delay(30)
  const share = mockShares.find((s) => s.id === id)
  if (!share) {
    throw new ApiError(404, { code: 'fs.not_found', message: 'not found' })
  }
  const healed: MockShare = { ...share, broken_reason: undefined }
  mockShares = mockShares.map((s) => (s.id === id ? healed : s))
  return shareOf(healed)
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
      // (`go/internal/core` invalid-path errors
      // -> `ErrorCode::FsInvalidName` -> `"fs.invalid_name"`,
      // `go/internal/httpapi/handler`) — this used to say
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
// `go/internal/auth`'s group CRUD closely enough to drive the admin UI in
// dev mode: `group_.name` is unique, deleting a group cascades to its
// memberships (and to the live grants table's own filter, same as deleting a
// share cascades to `mockGrants` above), and adding/removing a member refuses
// an unknown user id.

// Empty, one member, several: the member-count chip has to have something to
// vary against, and the empty-state panel is a different screen entirely.
let mockGroups: AdminGroup[] = [
  { id: 1, name: '개발팀', members: [1, 2, 3] },
  { id: 2, name: '경영지원', members: [4] },
  { id: 3, name: '외부 협력사 (2026 상반기)', members: [] }
]
let nextGroupId = 4

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
// `go/internal/httpapi/handler/shares.go` (
// `ShareLinkPatch` closely enough to drive the manage-links UI in dev mode:
// a link's `perms` defaults to read+download when the caller doesn't specify
// any (same default `go/internal/httpapi/handler/shares.go` applies),
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

/** The `done` event of `GET /api/search/stream`. `truncated` says the walk hit
 *  its deadline, so the hits that arrived are a prefix of the matches. */
export interface SearchDone {
  truncated: boolean
  /** Which index tier answered, for a diagnostic. Absent when the stream
   *  ended without a parsable payload. */
  tier?: string
}

function searchStream(query: string, onHit: (hit: SearchHit) => void, onDone: (done: SearchDone) => void): () => void {
  let cancelled = false
  const q = query.trim().toLowerCase()

  async function run() {
    if (!q) {
      onDone({ truncated: false })
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
    if (!cancelled) onDone({ truncated: false })
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
  archive,
  archiveList,
  folderSize,
  thumbUrl,
  recentList,
  jobList,
  jobStatus,
  jobCancel,
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
  wipeAppPassword,
  listSessions,
  revokeSession,
  updateSmbSettings,
  setSmbPassword,
  clearSmbPassword,
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
  adminSetRateSettings,
  adminSetNetworkSettings,
  adminSetDbSettings,
  adminSetSymlinkPolicySettings,
  adminSetHomesSettings,
  adminSetWatchSettings,
  adminSetOidcSettings,
  adminSetPathsSettings,
  adminListUsers,
  adminCreateUser,
  adminSetUserDisabled,
  adminSetUserQuota,
  adminSetUserPassword,
  adminDeleteUser,
  adminGetUserOidc,
  adminUnlinkUserOidc,
  adminListShares,
  adminCreateShare,
  adminUpdateShare,
  adminDeleteShare,
  adminRetryShare,
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
