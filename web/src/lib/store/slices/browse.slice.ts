import { createStore, type StoreApi } from 'zustand/vanilla'
import type { Entry, Order, SortKey } from '../../api/types'

export interface Sort {
  readonly key: SortKey
  readonly order: Order
}

export const MAX_CACHED_ROWS = 2000

export interface CacheState {
  readonly rows: ReadonlyMap<number, Entry>
  readonly nameIndex: ReadonlyMap<string, number>
  readonly lruQueue: readonly number[]
}

export interface BrowseSnapshot extends CacheState {
  readonly path: string
  readonly total: number
  readonly dirs: number
  readonly dirEtag: string | null
  readonly sort: Sort
  readonly loading: boolean
  readonly loadingWindow: boolean
  readonly selectedNames: ReadonlySet<string>
  readonly focusedIndex: number | null
  readonly anchor: string | null
  readonly nextCursor: string | null
  readonly walkComplete: boolean
  readonly walked: number
  readonly view: 'list' | 'grid'
  readonly density: 'compact' | 'comfortable' | 'spacious'
}

export function pureTouchLru(lruQueue: readonly number[], index: number): readonly number[] {
  const filtered = lruQueue.filter((i) => i !== index)
  return [...filtered, index]
}

export function findEvictionVictims(
  rows: ReadonlyMap<number, Entry>,
  lruQueue: readonly number[],
  selection: ReadonlySet<string>,
  maxRows = MAX_CACHED_ROWS
): readonly number[] {
  let count = rows.size
  if (count <= maxRows) return []

  const victims: number[] = []
  for (const idx of lruQueue) {
    if (count <= maxRows) break
    const entry = rows.get(idx)
    if (entry !== undefined && !selection.has(entry.name)) {
      victims.push(idx)
      count--
    }
  }
  return victims
}

export function pureEvictIfNeeded(
  cache: CacheState,
  selection: ReadonlySet<string>,
  maxRows = MAX_CACHED_ROWS
): CacheState {
  if (cache.rows.size <= maxRows) return cache

  const nextRows = new Map(cache.rows)
  const nextIndex = new Map(cache.nameIndex)
  const nextLru = [...cache.lruQueue]

  while (nextRows.size > maxRows) {
    const victimPos = nextLru.findIndex((idx) => {
      const entry = nextRows.get(idx)
      return entry !== undefined && !selection.has(entry.name)
    })
    if (victimPos === -1) break // all currently cached items are selected
    const idx = nextLru[victimPos]
    nextLru.splice(victimPos, 1)
    const entry = nextRows.get(idx)
    nextRows.delete(idx)
    if (entry) nextIndex.delete(entry.name)
  }

  return {
    rows: nextRows,
    nameIndex: nextIndex,
    lruQueue: nextLru
  }
}

export function pureSetRow(
  cache: CacheState,
  index: number,
  entry: Entry,
  selection: ReadonlySet<string>,
  maxRows = MAX_CACHED_ROWS
): CacheState {
  const nextRows = new Map(cache.rows)
  const nextIndex = new Map(cache.nameIndex)

  const occupant = nextRows.get(index)
  if (occupant && occupant.name !== entry.name) {
    nextIndex.delete(occupant.name)
  }

  const priorIndex = nextIndex.get(entry.name)
  if (priorIndex !== undefined && priorIndex !== index) {
    nextRows.delete(priorIndex)
  }

  nextRows.set(index, entry)
  nextIndex.set(entry.name, index)
  const nextLru = pureTouchLru(cache.lruQueue, index)

  return pureEvictIfNeeded(
    { rows: nextRows, nameIndex: nextIndex, lruQueue: nextLru },
    selection,
    maxRows
  )
}

export function pureApplyPage(
  cache: CacheState,
  offset: number,
  entries: readonly Entry[],
  selection: ReadonlySet<string>,
  maxRows = MAX_CACHED_ROWS
): CacheState {
  let cur = cache
  for (let i = 0; i < entries.length; i++) {
    cur = pureSetRow(cur, offset + i, entries[i], selection, maxRows)
  }
  return cur
}

export function pureDropUnselectedRows(
  cache: CacheState,
  selection: ReadonlySet<string>
): CacheState {
  const nextRows = new Map<number, Entry>()
  const nextIndex = new Map<string, number>()

  for (const [idx, entry] of cache.rows) {
    if (selection.has(entry.name)) {
      nextRows.set(idx, entry)
      nextIndex.set(entry.name, idx)
    }
  }

  const nextLru = cache.lruQueue.filter((idx) => nextRows.has(idx))
  return {
    rows: nextRows,
    nameIndex: nextIndex,
    lruQueue: nextLru
  }
}

export function createBrowseStore(initialPath = '/'): StoreApi<BrowseSnapshot> {
  return createStore<BrowseSnapshot>(() => ({
    path: initialPath,
    total: 0,
    dirs: 0,
    dirEtag: null,
    sort: { key: 'name', order: 'asc' },
    loading: false,
    loadingWindow: false,
    selectedNames: new Set<string>(),
    focusedIndex: null,
    anchor: null,
    nextCursor: null,
    walkComplete: false,
    walked: 0,
    view: 'list',
    density: 'comfortable',
    rows: new Map<number, Entry>(),
    nameIndex: new Map<string, number>(),
    lruQueue: []
  }))
}
