import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Entry } from '../api/types'
import { selection } from '../store/selection.store'
import { act, cleanup, fireEvent, render } from '../../test/test-utils'
import { FileGrid } from './FileGrid'
import { FileTable } from './FileTable'

const documents: Entry = {
  name: 'Documents',
  path: '/home/Documents',
  kind: 'dir',
  size: 0,
  mtime_ns: '0',
  etag: 'documents',
  etag_weak: false,
  perms: { read: true, write: false, create: false, delete: false, rename: false, move: false, share: false, download: false }
}
const pictures: Entry = { ...documents, name: 'Pictures', path: '/home/Pictures' }

// jsdom versions without PointerEvent otherwise discard pointerType and isPrimary.
function pointer(target: HTMLElement, type: string, options: PointerEventInit = {}) {
  const event = new MouseEvent(type, { bubbles: true, cancelable: true, clientX: 20, clientY: 20, ...options })
  Object.defineProperties(event, {
    pointerType: { value: options.pointerType ?? 'touch' },
    pointerId: { value: options.pointerId ?? 1 },
    isPrimary: { value: options.isPrimary ?? true }
  })
  fireEvent(target, event)
}

function tap(target: HTMLElement, options: PointerEventInit = {}) {
  pointer(target, 'pointerdown', options)
  pointer(target, 'pointerup', options)
  pointer(target, 'click', { detail: 1, ...options })
}

beforeEach(() => {
  selection.reset()
  vi.useFakeTimers()
  vi.setSystemTime(new Date('2026-01-01T00:00:00Z'))
})
afterEach(async () => {
  await act(async () => {})
  cleanup()
  selection.reset()
  vi.useRealTimers()
})

describe.each([
  ['list', FileTable, '.sc-row', '.sc-row__cell--select'],
  ['grid', FileGrid, '.sc-file-grid__card', '.sc-file-grid__check']
] as const)('%s file activation', (_name, View, entrySelector, checkSelector) => {
  function renderView(initialEntries: Entry[] = [documents, pictures]) {
    const opened: { path: string; selected: string[] }[] = []
    const onOpen = vi.fn((entry: Entry) => {
      opened.push({ path: entry.path, selected: [...selection.getState().names] })
      // Navigation clears selection. No delayed click may restore the old entry.
      selection.clear()
    })
    const onContextMenu = vi.fn()
    const requestMore = vi.fn()
    const props = { loading: false, loadingMore: false, requestMore, perms: documents.perms, onOpen, onContextMenu }
    const result = render(<View {...props} entries={initialEntries} total={initialEntries.length} dirs={initialEntries.length} />)
    return {
      ...result,
      opened,
      onOpen,
      onContextMenu,
      entries: () => Array.from(result.container.querySelectorAll<HTMLElement>(entrySelector)),
      async replaceEntries(entries: Entry[]) {
        await act(async () => {})
        result.rerender(<View {...props} entries={entries} total={entries.length} dirs={entries.length} />)
        await act(async () => {})
      }
    }
  }

  it('selects on mouse click and opens only on the second native click, not again on dblclick', () => {
    const view = renderView()
    const [row] = view.entries()
    tap(row, { pointerType: 'mouse', detail: 1 })
    expect(row.getAttribute('aria-selected')).toBe('true')
    expect(view.opened).toEqual([])

    tap(row, { pointerType: 'mouse', detail: 2 })
    expect(view.opened).toEqual([{ path: documents.path, selected: [documents.name] }])
    fireEvent.doubleClick(row)
    expect(view.onOpen).toHaveBeenCalledTimes(1)
    expect([...selection.getState().names]).toEqual([])
  })

  it('opens on the second touch click only after selection, without relying on dblclick', () => {
    const view = renderView()
    const [row] = view.entries()
    tap(row)
    expect(row.getAttribute('aria-selected')).toBe('true')
    expect(view.opened).toEqual([])

    vi.advanceTimersByTime(200)
    pointer(row, 'pointerdown', { pointerId: 2 })
    pointer(row, 'pointerup', { pointerId: 2 })
    expect(view.opened).toEqual([])
    pointer(row, 'click', { pointerId: 2, detail: 2 })
    expect(view.opened).toEqual([{ path: documents.path, selected: [documents.name] }])
    expect([...selection.getState().names]).toEqual([])
    fireEvent.doubleClick(row)
    expect(view.onOpen).toHaveBeenCalledTimes(1)
  })

  it.each(['mouse', 'touch'])('selects a different folder instead of carrying over the previous %s click', (pointerType) => {
    const view = renderView()
    const [a, b] = view.entries()
    tap(a, { pointerType, detail: 1 })
    tap(b, { pointerType, detail: 2 })
    fireEvent.doubleClick(b)
    expect(view.opened).toEqual([])
    expect(a.getAttribute('aria-selected')).toBe('false')
    expect(b.getAttribute('aria-selected')).toBe('true')

    tap(a, { pointerType, detail: 1 })
    expect(view.opened).toEqual([])
    expect(a.getAttribute('aria-selected')).toBe('true')
    expect(b.getAttribute('aria-selected')).toBe('false')

    tap(a, { pointerType, detail: 2 })
    tap(b, { pointerType, detail: 1 })
    tap(b, { pointerType, detail: 2 })
    expect(view.opened.map((entry) => entry.path)).toEqual([documents.path, pictures.path])
  })

  it('does not carry taps across same-name paths or impose a navigation cooldown', async () => {
    const view = renderView([documents])
    tap(view.entries()[0])
    const nested = { ...documents, path: '/home/Documents/Documents' }
    await view.replaceEntries([nested])
    tap(view.entries()[0])
    expect(view.opened).toEqual([])
    tap(view.entries()[0])
    expect(view.opened.map((entry) => entry.path)).toEqual([nested.path])

    const deeper = { ...documents, path: `${nested.path}/Documents` }
    await view.replaceEntries([deeper])
    tap(view.entries()[0])
    tap(view.entries()[0])
    expect(view.opened.map((entry) => entry.path)).toEqual([nested.path, deeper.path])
  })

  it.each(['cancelled', 'moved', 'held'] as const)('breaks a touch sequence after a %s gesture', (gesture) => {
    const view = renderView()
    const [row] = view.entries()
    tap(row)
    pointer(row, 'pointerdown')
    if (gesture === 'cancelled') pointer(row, 'pointercancel')
    if (gesture === 'moved') {
      pointer(row, 'pointermove', { clientX: 60 })
      pointer(row, 'pointermove', { clientX: 20 })
    }
    if (gesture === 'held') vi.advanceTimersByTime(500)
    pointer(row, 'pointerup')
    pointer(row, 'click', { detail: 2 })
    tap(row)
    expect(view.opened).toEqual([])
    tap(row)
    expect(view.opened.map((entry) => entry.path)).toEqual([documents.path])
  })

  it('keeps checkbox selection and menu interaction out of activation sequences', () => {
    const view = renderView()
    const [a, b] = view.entries()
    const checkbox = b.querySelector<HTMLElement>(checkSelector)!
    tap(a)
    tap(checkbox)
    expect(a.getAttribute('aria-selected')).toBe('true')
    expect(b.getAttribute('aria-selected')).toBe('true')
    tap(checkbox)
    fireEvent.doubleClick(checkbox)
    expect(b.getAttribute('aria-selected')).toBe('false')
    tap(a)
    expect(view.opened).toEqual([])

    const menu = a.querySelector<HTMLElement>('.sc-file-grid__kebab')
    if (menu) tap(menu)
    else fireEvent.contextMenu(a)
    expect(view.onContextMenu).toHaveBeenCalledTimes(1)
    tap(a)
    expect(view.opened).toEqual([])
    tap(a)
    expect(view.opened.map((entry) => entry.path)).toEqual([documents.path])
  })

  it('preserves modifier selection without turning mixed-modifier clicks or taps into opens', () => {
    const view = renderView()
    const [a, b] = view.entries()
    tap(a, { pointerType: 'mouse', detail: 1 })
    tap(b, { pointerType: 'mouse', detail: 2, ctrlKey: true })
    fireEvent.doubleClick(b, { ctrlKey: true })
    expect([...selection.getState().names]).toEqual([documents.name, pictures.name])
    expect(view.opened).toEqual([])

    tap(b, { pointerType: 'mouse', detail: 2 })
    expect(view.opened).toEqual([])
    act(() => selection.clear())
    tap(a, { shiftKey: true })
    tap(a)
    expect(view.opened).toEqual([])
    tap(a)
    expect(view.opened.map((entry) => entry.path)).toEqual([documents.path])
  })
})
