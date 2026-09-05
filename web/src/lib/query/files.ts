// Directory listings, file reads, and every write that changes them.
//
// A listing is a cursor walk, so it is an infinite query: one page per request,
// appended in order. What used to be a sparse index-keyed cache with its own
// LRU is now just `pages`, and "load more" is `fetchNextPage`.
import { infiniteQueryOptions, mutationOptions, queryOptions } from '@tanstack/svelte-query'
import { api } from '../api/client'
import {
  permsFromNames,
  type Entry,
  type ListResponse,
  type MoveReq,
  type OnConflict,
  type Perms
} from '../api/types'
import { isWithin, parentOf } from '../api/path-utils'
import { queryClient } from './client'
import { keys, type Sort } from './keys'

/** One request's worth of rows. Large enough that the first screen is one
 *  round trip, small enough that a scroll does not stall on it. */
export const PAGE_LIMIT = 200

export function dirListQuery(path: string, sort: Sort) {
  return infiniteQueryOptions({
    queryKey: keys.pathList(path, sort),
    queryFn: ({ pageParam, signal }) =>
      api.list(path, { sort: sort.key, order: sort.order, cursor: pageParam ?? undefined, limit: PAGE_LIMIT, signal }),
    initialPageParam: null as string | null,
    getNextPageParam: (last: ListResponse) => last.cursor,
    // The WebSocket says when this directory changed, so time cannot.
    staleTime: Infinity
  })
}

/** A listing flattened for rendering: the rows loaded so far plus the whole
 *  directory's shape, which every page repeats and only the newest is true for. */
export interface DirView {
  readonly entries: readonly Entry[]
  readonly total: number
  /** How many of `total` are folders, and so the index files start at. The
   *  grid lays out two separately windowed sections and cannot derive it. */
  readonly dirs: number
  readonly etag: string | null
  /** What this account may do to the directory itself. Nothing in the rows
   *  answers "may I create a file here". */
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

export function statQuery(path: string) {
  return queryOptions({ queryKey: keys.pathStat(path), queryFn: () => api.stat(path) })
}

export function folderSizeQuery(path: string, enabled = true) {
  return queryOptions({
    queryKey: keys.pathSize(path),
    queryFn: () => api.folderSize(path),
    enabled,
    // A recursive walk is expensive and the WebSocket invalidates it, so it
    // is never refetched on a whim.
    staleTime: Infinity
  })
}

export function fileContentQuery(entry: Entry | null | undefined) {
  return queryOptions({
    queryKey: keys.pathContent(entry?.path ?? ''),
    queryFn: () => api.readFile(entry as Entry),
    enabled: entry !== null && entry !== undefined,
    staleTime: Infinity
  })
}

export function archiveEntriesQuery(path: string, enabled = true) {
  return queryOptions({
    queryKey: keys.pathArchive(path),
    queryFn: () => api.archiveList(path),
    enabled,
    staleTime: Infinity
  })
}

export function recentQuery(limit = 100) {
  return queryOptions({ queryKey: [...keys.recent(), limit], queryFn: () => api.recentList({ limit }) })
}

export function trashQuery() {
  return queryOptions({ queryKey: keys.trash(), queryFn: () => api.trashList() })
}

/**
 * Everything read at or below these paths is now wrong.
 *
 * A change to a folder reaches the entries inside it too: a file's own `stat`
 * and its contents are keyed by its own path, so they are matched rather than
 * named. Marking an unobserved query stale costs nothing, and missing one
 * leaves a stale row on screen.
 */
export function invalidateDirs(paths: Iterable<string>): void {
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

/** The same, aimed at entries: each one's own reads plus the folder listing it
 *  appears in. */
function invalidateEntries(paths: Iterable<string>): void {
  invalidateDirs([...paths, ...[...paths].map(parentOf)])
}

export function mkdirMutation() {
  return mutationOptions({
    mutationFn: ({ parent, name }: { parent: string; name: string }) => api.mkdir(`${parent === '/' ? '' : parent}/${name}`),
    onSuccess: (_entry, { parent }) => invalidateDirs([parent])
  })
}

export function renameMutation() {
  return mutationOptions({
    mutationFn: ({ path, newName }: { path: string; newName: string }) => api.rename(path, newName),
    onSuccess: (_entry, { path }) => invalidateEntries([path])
  })
}

export function deleteMutation() {
  return mutationOptions({
    mutationFn: (paths: string[]) => api.delete(paths),
    onSuccess: (_result, paths) => {
      invalidateEntries(paths)
      void queryClient.invalidateQueries({ queryKey: keys.trash() })
    }
  })
}

export interface TransferVars {
  readonly paths: string[]
  readonly dest: string
  readonly onConflict: OnConflict
}

function transferRequest({ paths, dest, onConflict }: TransferVars): MoveReq {
  return { paths, dest, on_conflict: onConflict }
}

export function moveMutation() {
  return mutationOptions({
    mutationFn: (vars: TransferVars) => api.move(transferRequest(vars)),
    onSuccess: (_result, { paths, dest }) => {
      invalidateEntries(paths)
      invalidateDirs([dest])
    }
  })
}

export function copyMutation() {
  return mutationOptions({
    mutationFn: (vars: TransferVars) => api.copy(transferRequest(vars)),
    onSuccess: (result, { dest }) => {
      invalidateDirs([dest])
      // A copy large enough to run in the background reports through the job
      // list, so the tray has to learn about it now rather than on its next poll.
      if (result.job) void queryClient.invalidateQueries({ queryKey: keys.jobs() })
    }
  })
}

export function writeFileMutation() {
  return mutationOptions({
    mutationFn: ({ path, content }: { path: string; content: string }) => api.writeFile(path, content),
    onSuccess: (_entry, { path }) => invalidateEntries([path])
  })
}

export function trashRestoreMutation() {
  return mutationOptions({
    mutationFn: (ids: string[]) => api.trashRestore(ids),
    // Restored items land back wherever they came from, which the response
    // does not name, so every listing is suspect.
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: keys.trash() })
      void queryClient.invalidateQueries({ queryKey: ['path'] })
    }
  })
}

export function trashPurgeMutation() {
  return mutationOptions({
    mutationFn: (ids: string[]) => api.trashPurge(ids),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: keys.trash() })
  })
}

/** Both of these mint a one-shot ticket the browser then navigates to; they
 *  change nothing, so nothing is invalidated. */
export function archiveTicketMutation() {
  return mutationOptions({ mutationFn: ({ paths, name }: { paths: string[]; name?: string }) => api.archive(paths, name) })
}
