import { describe, expect, it } from 'vitest'
import type { Entry } from '../../api/types'
import {
  createBrowseStore,
  pureApplyPage,
  pureDropUnselectedRows,
  pureEvictIfNeeded,
  pureSetRow,
  type CacheState
} from './browse.slice'

function makeEntry(name: string, size = 100): Entry {
  return {
    name,
    path: `/${name}`,
    kind: 'file',
    size,
    mtime_ns: '1000',
    etag: 'tag',
    etag_weak: false,
    perms: { read: true, write: true, create: true, delete: true, rename: true, move: true, share: true, download: true }
  }
}

describe('pure browse cache and LRU eviction', () => {
  it('sets rows and maintains name index', () => {
    const empty: CacheState = { rows: new Map(), nameIndex: new Map(), lruQueue: [] }
    const file1 = makeEntry('file1.txt')
    const updated = pureSetRow(empty, 0, file1, new Set())

    expect(updated.rows.get(0)?.name).toBe('file1.txt')
    expect(updated.nameIndex.get('file1.txt')).toBe(0)
    expect(updated.lruQueue).toEqual([0])
  })

  it('evicts oldest unselected rows when exceeding maxRows bound', () => {
    let cache: CacheState = { rows: new Map(), nameIndex: new Map(), lruQueue: [] }
    const bound = 3

    // Fill up to bound
    cache = pureSetRow(cache, 0, makeEntry('a.txt'), new Set(), bound)
    cache = pureSetRow(cache, 1, makeEntry('b.txt'), new Set(), bound)
    cache = pureSetRow(cache, 2, makeEntry('c.txt'), new Set(), bound)

    expect(cache.rows.size).toBe(3)

    // Add 4th item; row 0 ('a.txt') should be evicted
    cache = pureSetRow(cache, 3, makeEntry('d.txt'), new Set(), bound)
    expect(cache.rows.size).toBe(3)
    expect(cache.rows.has(0)).toBe(false)
    expect(cache.nameIndex.has('a.txt')).toBe(false)
    expect(cache.rows.has(3)).toBe(true)
  })

  it('protects selected entries from being evicted even if they are oldest', () => {
    let cache: CacheState = { rows: new Map(), nameIndex: new Map(), lruQueue: [] }
    const bound = 3

    cache = pureSetRow(cache, 0, makeEntry('a.txt'), new Set(), bound)
    cache = pureSetRow(cache, 1, makeEntry('b.txt'), new Set(), bound)
    cache = pureSetRow(cache, 2, makeEntry('c.txt'), new Set(), bound)

    // 'a.txt' is selected, so adding a 4th item must evict 'b.txt' instead
    const selection = new Set(['a.txt'])
    cache = pureSetRow(cache, 3, makeEntry('d.txt'), selection, bound)

    expect(cache.rows.size).toBe(3)
    expect(cache.rows.has(0)).toBe(true) // 'a.txt' survived eviction
    expect(cache.rows.has(1)).toBe(false) // 'b.txt' was evicted
    expect(cache.rows.has(2)).toBe(true)
    expect(cache.rows.has(3)).toBe(true)
  })

  it('drops unselected rows cleanly', () => {
    let cache: CacheState = { rows: new Map(), nameIndex: new Map(), lruQueue: [] }
    cache = pureSetRow(cache, 0, makeEntry('a.txt'), new Set())
    cache = pureSetRow(cache, 1, makeEntry('b.txt'), new Set())

    const kept = pureDropUnselectedRows(cache, new Set(['b.txt']))
    expect(kept.rows.size).toBe(1)
    expect(kept.rows.has(1)).toBe(true)
    expect(kept.nameIndex.has('a.txt')).toBe(false)
  })

  it('applies an entire page of entries', () => {
    const empty: CacheState = { rows: new Map(), nameIndex: new Map(), lruQueue: [] }
    const page = [makeEntry('p1.txt'), makeEntry('p2.txt')]
    const applied = pureApplyPage(empty, 10, page, new Set())

    expect(applied.rows.size).toBe(2)
    expect(applied.rows.get(10)?.name).toBe('p1.txt')
    expect(applied.rows.get(11)?.name).toBe('p2.txt')
  })
})

describe('createBrowseStore', () => {
  it('creates store with initial path', () => {
    const store = createBrowseStore('/initial')
    expect(store.getState().path).toBe('/initial')
    expect(store.getState().rows.size).toBe(0)
  })
})
