// Directory listings, file reads, and every write that changes them.
import {
  useInfiniteQuery,
  useMutation,
  useQueries,
  useQuery,
  useQueryClient,
  type QueryClient
} from '@tanstack/react-query'
import { useCallback, useMemo } from 'react'
import {
  ApiError,
  apiUrl,
  client,
  decimal,
  ensureOk,
  malformed,
  oneOf,
  send,
  untimed,
  unwrap,
  unwrapEmpty,
  type ApiErrorBody
} from '../../api/fetcher'
import type { components } from '../../api/generated/schema'
import { isWithin, parentOf } from '../../lib/path-utils'
import { decryptDownload, encryptForUpload } from '../../lib/crypto/e2ee'
import { encryptionForLabel, shareLabelOf } from '../shares/encrypted-shares'
import { keys, type Sort } from '../../api/query-keys'
import { permsFromNames, type Perms } from './perms'

type Schemas = components['schemas']

export type Kind = 'file' | 'dir' | 'symlink' | 'other'

const KINDS: readonly Kind[] = ['file', 'dir', 'symlink', 'other']

export interface Entry {
  name: string
  /** Opaque references to the row's bytes and preview. Build URLs from them
   *  with `contentUrl` and `thumbUrl`, never from `path`. `content` is absent
   *  on a directory, `thumb` on a file the decoder does not recognise. */
  content?: string
  thumb?: string
  /** `{label}/rest`, as the read and stat routes take it. */
  path: string
  kind: Kind
  /** Bytes; for a directory, the recursive rollup the server keeps. */
  size: number
  mtime_ns: string
  /** Absent where the filesystem has no birth time. */
  btime_ns?: string
  etag: string
  /** The server derives tokens from metadata that can repeat, so it refuses a
   *  conditional write against a weak one. The editor compares a fresh stat
   *  instead. */
  etag_weak: boolean
  perms: Perms
  preview?: { available: boolean }
}

export type SortKey = 'name' | 'size' | 'mtime' | 'kind'
export type Order = 'asc' | 'desc'

export interface ListOpts {
  sort?: SortKey
  order?: Order
  /** The previous page's `cursor`. Opaque: there is no offset into a listing. */
  cursor?: string
  limit?: number
  signal?: AbortSignal
}

export interface ListResponse {
  total: number
  /** How many of `total` are folders. Folders sort first in either direction,
   *  so this is also the index where files start, which the grid cannot see
   *  when the rows either side of the split are not loaded. */
  dirs: number
  /** Null on the final page. */
  cursor: string | null
  entries: Entry[]
  dir_etag: string
  dir_etag_weak: boolean
  /** What the caller may do to the directory itself; the rows cannot say
   *  whether a file may be created beside them. */
  dir_perms: string[]
}

/** What a move or copy does when the destination name is taken. */
export type OnConflict = 'fail' | 'rename' | 'overwrite' | 'skip'

/** One requested item's outcome. `destination` can differ from the request
 *  when `rename` added a suffix. `started` and `job` describe an accepted
 *  asynchronous copy, `copied` a move that crossed devices, and `skipped` a
 *  deliberate no-op. */
export interface BatchItemResult {
  path: string
  ok: boolean
  error?: ApiErrorBody['error']
  destination?: string
  started?: boolean
  skipped?: boolean
  copied?: boolean
  job?: string
}

export interface BatchResult {
  results: BatchItemResult[]
}

export interface CopyResult {
  results: BatchItemResult[]
  /** Every copy job the server accepted, in request order. */
  jobs?: string[]
}

/** No directory count: the server keeps one recursive count. */
export interface FolderSize {
  bytes: number
  files: number
}

/** One entry of a ZIP listing. Not openable: the server does not extract. */
export interface ArchiveEntry {
  name: string
  size: number
  kind: 'file' | 'dir'
}

export interface ArchiveListing {
  entries: ArchiveEntry[]
  /** The listing stopped at `limit`, so this is a prefix of the archive. */
  truncated: boolean
  limit: number
  /** Entries left out because their names cannot be handed out safely. */
  skipped?: number
}

/** A one-shot download ticket. The browser navigates to `url`, which the
 *  server builds, so the bytes never pass through the tab. */
export interface Ticket {
  token: string
  name: string
  url: string
}

export interface ReadFileResponse {
  content: string
}

function entryFromWire(w: Schemas['EntryView']): Entry {
  return {
    name: w.name,
    path: w.path,
    kind: oneOf(w.kind, KINDS, 'entry kind', 'other'),
    size: decimal(w.size, 'entry size'),
    mtime_ns: w.mtime_ns,
    etag: w.etag,
    etag_weak: w.etag_weak,
    perms: permsFromNames(w.perms),
    // Each optional field is copied by name: dropping one compiles and only
    // shows on screen, as a grid of generic icons.
    ...(w.btime_ns === undefined ? {} : { btime_ns: w.btime_ns }),
    ...(w.preview === undefined ? {} : { preview: { available: w.preview.available === true } }),
    ...(w.content === undefined ? {} : { content: w.content }),
    ...(w.thumb === undefined ? {} : { thumb: w.thumb })
  }
}

function listFromWire(w: Schemas['PageView']): ListResponse {
  return {
    entries: (w.entries ?? []).map(entryFromWire),
    dirs: w.dirs,
    total: w.total,
    cursor: w.cursor ? w.cursor : null,
    dir_etag: w.dir_etag,
    dir_etag_weak: w.dir_etag_weak,
    // Absent at the virtual root, which is a projection of grants rather than
    // a folder anything can be done to.
    dir_perms: w.dir_perms ?? []
  }
}

export async function listPage(path: string, opts: ListOpts = {}): Promise<ListResponse> {
  const { sort, order, cursor, limit, signal } = opts
  const query = { path, cursor, sort, order, limit: limit === undefined ? undefined : String(limit) }
  return listFromWire(await unwrap(client.GET('/api/v1/files/list', { params: { query }, signal })))
}

export async function stat(path: string): Promise<Entry> {
  return entryFromWire(await unwrap(client.GET('/api/v1/files/stat', { params: { query: { path } } })))
}

async function mkdir(path: string): Promise<Entry> {
  return entryFromWire(await unwrap(client.POST('/api/v1/files/mkdir', { body: { path } })))
}

async function rename(path: string, newName: string): Promise<Entry> {
  return entryFromWire(await unwrap(client.POST('/api/v1/files/rename', { body: { path, new_name: newName } })))
}

function errorBodyOf(err: unknown): ApiErrorBody['error'] {
  if (err instanceof ApiError) return { code: err.code, message: err.message, detail: err.detail }
  return { code: 'internal', message: String(err) }
}

/** Where one item lands when moved or copied into `dest`: its own name there. */
function joinDest(dest: string, source: string): string {
  const name = source.split('/').filter(Boolean).pop() ?? source
  return `${dest.replace(/\/+$/, '')}/${name}`
}

/** Runs `one` for each path in sequence and records each outcome against its
 *  path. Sequential because a burst of destructive requests against one
 *  directory is what a rate limit turns into half a selection failing. */
async function eachPath(
  paths: readonly string[],
  one: (path: string) => Promise<Omit<BatchItemResult, 'path' | 'ok'>>
): Promise<BatchItemResult[]> {
  const results: BatchItemResult[] = []
  for (const path of paths) {
    try {
      results.push({ path, ok: true, ...(await one(path)) })
    } catch (err) {
      results.push({ path, ok: false, error: errorBodyOf(err) })
    }
  }
  return results
}

export interface TransferVars {
  readonly paths: readonly string[]
  readonly dest: string
  readonly onConflict: OnConflict
}

async function copy({ paths, dest, onConflict }: TransferVars): Promise<CopyResult> {
  const results = await eachPath(paths, async (path) => {
    const body = { from: path, to: joinDest(dest, path), on_conflict: onConflict }
    const out = await unwrap(client.POST('/api/v1/files/copy', { body }))
    return {
      destination: out.path,
      started: out.started,
      skipped: out.skipped,
      ...(out.started && out.id ? { job: out.id } : {})
    }
  })
  const jobs = results.flatMap((item) => (item.job === undefined ? [] : [item.job]))
  return { results, ...(jobs.length > 0 ? { jobs } : {}) }
}

async function move({ paths, dest, onConflict }: TransferVars): Promise<BatchResult> {
  const results = await eachPath(paths, async (path) => {
    const body = { from: path, to: joinDest(dest, path), on_conflict: onConflict }
    const out = await unwrap(client.POST('/api/v1/files/move', { body }))
    return { destination: out.path, copied: out.copied, skipped: out.skipped }
  })
  return { results }
}

async function remove(paths: readonly string[]): Promise<BatchResult> {
  const results = await eachPath(paths, async (path) => {
    await unwrapEmpty(client.POST('/api/v1/files/delete', { body: { path } }))
    return {}
  })
  return { results }
}

/** A ticket for a selection. The request cannot be the download itself: it is
 *  a POST with a CSRF header, which a browser cannot navigate to. */
async function archive(paths: readonly string[], name?: string): Promise<Ticket> {
  return unwrap(client.POST('/api/v1/files/archive', { body: { paths: [...paths], name }, fetch: untimed }))
}

/** The same ticket for one file. A folder is refused with 422. */
export async function download(path: string): Promise<Ticket> {
  return unwrap(client.POST('/api/v1/files/download', { body: { path } }))
}

/** A ZIP's entries. A path the caller cannot list and a file that is not a
 *  zip are the same 404. */
async function archiveList(path: string): Promise<ArchiveListing> {
  const w = await unwrap(client.GET('/api/v1/files/archive/list', { params: { query: { path } } }))
  const entries = (w.entries ?? []).map((e): ArchiveEntry => ({
    name: e.name,
    size: decimal(e.size, 'archive entry size'),
    kind: e.is_dir ? 'dir' : 'file'
  }))
  // The server reports what it packed rather than the ceiling it was given.
  return { entries, truncated: w.truncated === true, limit: entries.length, skipped: w.skipped }
}

/** A row's preview URL, or '' when it has none. `dim` picks one of the
 *  server's fixed boxes, so each layout does not get its own cache entry. */
export function thumbUrl(entry: Pick<Entry, 'thumb'>, dim: number): string {
  if (!entry.thumb) return ''
  const size = dim <= 256 ? 'small' : dim <= 512 ? 'medium' : 'large'
  return apiUrl('/files/thumbnail', { claim: entry.thumb, size })
}

/** A row's own bytes, inline, or '' for a directory. */
export function contentUrl(entry: Pick<Entry, 'content'>): string {
  if (!entry.content) return ''
  return apiUrl('/files/read', { claim: entry.content })
}

/** One folder's recursive size. A folder holding a subtree the account is
 *  denied answers 403 rather than a total covering data it cannot read. */
async function folderSize(path: string): Promise<FolderSize> {
  const w = await unwrap(client.GET('/api/v1/files/size', { params: { query: { path } } }))
  return { bytes: decimal(w.size, 'folder size'), files: decimal(w.count, 'folder file count') }
}

/** One file's text. On an encrypted share the bytes are ciphertext, so they
 *  are decrypted before the UTF-8 decode. Invalid UTF-8 becomes replacement
 *  characters: the editor saves conditionally, so a misread cannot be written
 *  back silently. */
export async function readFile(entry: Pick<Entry, 'content' | 'path'>): Promise<ReadFileResponse> {
  const res = await send(contentUrl(entry), { signal: null })
  await ensureOk(res)
  const encryption = await encryptionForLabel(shareLabelOf(entry.path))
  if (!encryption) return { content: await res.text() }
  const plaintext = await decryptDownload(await res.arrayBuffer(), encryption.salt)
  return { content: new TextDecoder().decode(plaintext) }
}

/** Replaces a file's content, unconditionally: every token this server mints
 *  is weak, and it refuses a conditional write against a weak token. An
 *  encrypted share gets the whole file as one rclone-crypt file. */
async function writeFile(path: string, content: string): Promise<Entry> {
  const encryption = await encryptionForLabel(shareLabelOf(path))
  let body: BodyInit = content
  if (encryption) {
    const encrypted = await encryptForUpload(new TextEncoder().encode(content), encryption.salt)
    // fetch's typing accepts only a typed array over a plain ArrayBuffer.
    const buf = new ArrayBuffer(encrypted.byteLength)
    new Uint8Array(buf).set(encrypted)
    body = buf
  }
  // The path rides in the query so the body is the file alone.
  const res = await send(apiUrl('/files/write', { path }), {
    method: 'POST',
    headers: { 'Content-Type': 'application/octet-stream' },
    body,
    signal: null
  })
  await ensureOk(res)
  const wire: unknown = await res.json().catch(() => {
    throw malformed(res.status, 'a body that is not JSON')
  })
  if (wire === null || typeof wire !== 'object') throw malformed(res.status, 'no entry')
  return entryFromWire(wire as Schemas['EntryView'])
}

/** One request's worth of rows: the first screen in one round trip, without a
 *  scroll stalling on it. */
export const PAGE_LIMIT = 200

export function useDirectory(path: string, sort: Sort) {
  return useInfiniteQuery({
    queryKey: keys.pathList(path, sort),
    queryFn: ({ pageParam, signal }) =>
      listPage(path, { sort: sort.key, order: sort.order, cursor: pageParam ?? undefined, limit: PAGE_LIMIT, signal }),
    initialPageParam: null as string | null,
    getNextPageParam: (last: ListResponse) => last.cursor,
    // The WebSocket says when this directory changed, so time cannot.
    staleTime: Infinity
  })
}

/** A listing flattened for rendering: the rows loaded so far plus the
 *  directory's shape, which every page repeats and only the newest is true for. */
export interface DirView {
  readonly entries: readonly Entry[]
  readonly total: number
  readonly dirs: number
  readonly etag: string | null
  readonly perms: Perms
}

const EMPTY_DIR: DirView = { entries: [], total: 0, dirs: 0, etag: null, perms: permsFromNames([]) }

export function dirViewOf(pages: readonly ListResponse[] | undefined): DirView {
  const newest = pages?.at(-1)
  if (pages === undefined || newest === undefined) return EMPTY_DIR
  const total = newest.total
  return {
    entries: pages.flatMap((p) => p.entries),
    total,
    // A response that somehow lacks it must not propagate NaN into a row count.
    dirs: Number.isFinite(newest.dirs) ? Math.min(Math.max(0, Math.floor(newest.dirs)), Math.max(0, total)) : 0,
    etag: newest.dir_etag,
    perms: permsFromNames(newest.dir_perms)
  }
}

/**
 * The encryption row covering a path's share, or null when the share is not
 * encrypted. A listing needs it to label sizes, which on the wire are
 * ciphertext sizes. `encryptionForLabel` fails closed, so a failed lookup
 * leaves this in error rather than reporting "not encrypted".
 */
export function useShareEncryption(path: string, enabled = true) {
  const label = shareLabelOf(path)
  return useQuery({
    queryKey: keys.shareEncryption(label),
    queryFn: () => encryptionForLabel(label),
    enabled: enabled && label !== '',
    staleTime: Infinity
  })
}

export function useStat(path: string, enabled = true) {
  return useQuery({ queryKey: keys.pathStat(path), queryFn: () => stat(path), enabled })
}

export function useFolderSizes(paths: readonly string[]) {
  return useQueries({
    queries: paths.map((path) => ({
      queryKey: keys.pathSize(path),
      queryFn: () => folderSize(path),
      // A recursive walk is expensive and the WebSocket invalidates it.
      staleTime: Infinity
    }))
  })
}

/** One file's text. `unlocked` is in the key so a read that failed while the
 *  share was locked is not served after the unlock, and the ETag so a later
 *  version at the same path is not answered with older text. */
function contentQuery(entry: Entry | null | undefined, unlocked: boolean, enabled: boolean) {
  return {
    queryKey: keys.pathContent(entry?.path ?? '', entry?.etag ?? '', unlocked),
    queryFn: () => readFile(entry as Entry),
    enabled: enabled && entry !== null && entry !== undefined,
    staleTime: Infinity
  }
}

export function useFileContent(entry: Entry | null | undefined, unlocked: boolean, enabled = true) {
  return useQuery(contentQuery(entry, unlocked, enabled))
}

export function useArchiveEntries(path: string, enabled = true) {
  return useQuery({
    queryKey: keys.pathArchive(path),
    queryFn: () => archiveList(path),
    enabled,
    staleTime: Infinity
  })
}

/**
 * Marks every read at or below these paths stale. A file's own stat and text
 * are keyed by its own path, so they are matched by prefix rather than named.
 * Marking an unobserved query costs nothing; missing one leaves a stale row.
 */
export function invalidateDirs(queryClient: QueryClient, paths: Iterable<string>): void {
  const roots = [...new Set(paths)]
  if (roots.length === 0) return
  void queryClient.invalidateQueries({
    predicate: (query) => {
      const [kind, subject] = query.queryKey
      if (kind !== 'path' || typeof subject !== 'string') return false
      return roots.some((root) => isWithin(subject, root))
    }
  })
}

/** Marks every path read stale, for a change that does not say where it
 *  landed, such as a background job ending. */
export function useInvalidateAllPaths(): () => void {
  const queryClient = useQueryClient()
  return useCallback(() => void queryClient.invalidateQueries({ queryKey: keys.paths() }), [queryClient])
}

/** The same, aimed at entries: each one's own reads plus the folder listing it. */
function invalidateEntries(queryClient: QueryClient, paths: readonly string[]): void {
  invalidateDirs(queryClient, [...paths, ...paths.map(parentOf)])
}

export function useMkdir() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ parent, name }: { parent: string; name: string }) =>
      mkdir(`${parent === '/' ? '' : parent}/${name}`),
    onSuccess: (_entry, { parent }) => invalidateDirs(queryClient, [parent])
  })
}

export function useRename() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ path, newName }: { path: string; newName: string }) => rename(path, newName),
    onSuccess: (_entry, { path }) => invalidateEntries(queryClient, [path])
  })
}

export function useDeleteFiles() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (paths: readonly string[]) => remove(paths),
    onSuccess: (_result, paths) => {
      invalidateEntries(queryClient, paths)
      void queryClient.invalidateQueries({ queryKey: keys.trash() })
    }
  })
}

export function useMoveFiles() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: move,
    onSuccess: (_result, { paths, dest }) => {
      invalidateEntries(queryClient, paths)
      invalidateDirs(queryClient, [dest])
    }
  })
}

export function useCopyFiles() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: copy,
    onSuccess: (result, { dest }) => {
      invalidateDirs(queryClient, [dest])
      // The tray reattaches every returned job id from the collection.
      if ((result.jobs?.length ?? 0) > 0) void queryClient.invalidateQueries({ queryKey: keys.jobs() })
    }
  })
}

export function useWriteFile() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ path, content }: { path: string; content: string }) => writeFile(path, content),
    onSuccess: (_entry, { path }) => invalidateEntries(queryClient, [path])
  })
}

/** Mints a ticket the browser then navigates to. Nothing changes, so nothing
 *  is invalidated. */
export function useArchiveTicket() {
  return useMutation({
    mutationFn: ({ paths, name }: { paths: readonly string[]; name?: string }) => archive(paths, name)
  })
}

/** Direct access to the cached stat and text of files, for flows that must
 *  read them outside a render: the editor's save and a link's target. */
export interface FileCache {
  /** A stat; `fresh` skips the cache for a decision that must see the file now. */
  fetchStat(path: string, fresh: boolean): Promise<Entry>
  /** The cached stat, unless something has marked it stale. */
  cachedStat(path: string): Entry | undefined
  fetchContent(entry: Entry, unlocked: boolean): Promise<ReadFileResponse>
  cachedContent(path: string, etag: string, unlocked: boolean): ReadFileResponse | undefined
  /** Records a save: the stat it answered and the text the file now holds. */
  rememberSaved(path: string, updated: Entry, content: string): void
  /** Forgets every text read at a path. */
  forgetContent(path: string): void
}

export function useFileCache(): FileCache {
  const queryClient = useQueryClient()
  return useMemo(
    () => ({
      fetchStat: (path, fresh) =>
        queryClient.fetchQuery({
          queryKey: keys.pathStat(path),
          queryFn: () => stat(path),
          staleTime: fresh ? 0 : Infinity
        }),
      cachedStat: (path) => {
        const state = queryClient.getQueryState<Entry>(keys.pathStat(path))
        return state?.isInvalidated ? undefined : state?.data
      },
      fetchContent: (entry, unlocked) => queryClient.fetchQuery(contentQuery(entry, unlocked, true)),
      cachedContent: (path, etag, unlocked) =>
        queryClient.getQueryData<ReadFileResponse>(keys.pathContent(path, etag, unlocked)),
      rememberSaved: (path, updated, content) => {
        queryClient.setQueryData(keys.pathContent(updated.path, updated.etag, true), { content })
        queryClient.setQueryData(keys.pathStat(path), updated)
      },
      forgetContent: (path) => queryClient.removeQueries({ queryKey: ['path', path, 'content'] })
    }),
    [queryClient]
  )
}
