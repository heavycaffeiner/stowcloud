import { describe, expect, it, vi } from 'vitest'
import type { Entry } from '../api/client'
import { rowActions, type RowActionHandlers } from './row-actions'

function entry(name: string, kind: 'file' | 'dir', share = true): Entry {
  return {
    name,
    kind,
    size: 1,
    mtime_ns: '0',
    etag: 'e',
    perms: { read: true, write: true, create: true, delete: true, rename: true, move: true, share, download: true },
    id: undefined
  } as Entry
}

const handlers: RowActionHandlers = {
  openInEditor: vi.fn(),
  download: vi.fn(),
  share: vi.fn(),
  rename: vi.fn(),
  transfer: vi.fn(),
  duplicate: vi.fn(),
  remove: vi.fn()
}

const keys = (targets: Entry[]) => rowActions(targets, handlers).map((a) => a.key)

describe('rowActions', () => {
  // The regression this file exists for. The right-click menu and the selection
  // bar both render `rowActions(browse.selected, ...)`, so the only way they can
  // disagree is if the function is called twice with different arguments. It is
  // not: the page derives one array and both `{#each}` over it. This asserts the
  // property that makes that safe -- one input, one answer, in one order -- so a
  // change that adds an action to only one surface has to come through here.
  it('answers the same list, in the same order, for the same target set', () => {
    const targets = [entry('a.txt', 'file')]
    const menu = rowActions(targets, handlers)
    const bar = rowActions(targets, handlers)
    expect(menu.map((a) => a.key)).toEqual(bar.map((a) => a.key))
    expect(menu.map((a) => a.label)).toEqual(bar.map((a) => a.label))
  })

  it('offers everything for a single file', () => {
    expect(keys([entry('a.txt', 'file')])).toEqual([
      'edit',
      'download',
      'share',
      'rename',
      'transfer',
      'duplicate',
      'delete'
    ])
  })

  it('drops the editor for a folder but keeps the rest', () => {
    expect(keys([entry('sub', 'dir')])).toEqual([
      'download',
      'share',
      'rename',
      'transfer',
      'duplicate',
      'delete'
    ])
  })

  // Rule 1: an action that can only mean one thing at a time needs exactly one
  // target. Reading `selected[0]` instead is what used to offer "open in the
  // text editor" for a mixed selection whose first row happened to be a file.
  it('hides the single-target actions once more than one row is selected', () => {
    expect(keys([entry('a.txt', 'file'), entry('b.txt', 'file')])).toEqual([
      'download',
      'transfer',
      'duplicate',
      'delete'
    ])
    expect(keys([entry('a.txt', 'file'), entry('sub', 'dir')])).toEqual([
      'download',
      'transfer',
      'duplicate',
      'delete'
    ])
  })

  // Rule 2: a permission-gated action needs every target to carry it.
  it('hides share when the target cannot be shared', () => {
    expect(keys([entry('a.txt', 'file', false)])).not.toContain('share')
  })

  it('offers nothing at all for an empty set', () => {
    expect(keys([])).toEqual([])
  })
})
