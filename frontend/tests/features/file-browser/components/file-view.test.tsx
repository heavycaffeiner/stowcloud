import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { FileEntry } from '../../../../src/features/file-browser/model/file-entry'
import { selection, useSelectionStore } from '../../../../src/features/file-browser/model/selection'
import { act, cleanup, fireEvent, render, within } from '../../../../src/test/test-utils'
import { FileGrid } from '../../../../src/features/file-browser/components/FileGrid'
import { FileList } from '../../../../src/features/file-browser/components/FileList'

const documents: FileEntry = { id: '/home/Documents', name: 'Documents', type: 'folder', size: 0, modifiedAt: 0 }
const pictures: FileEntry = { ...documents, id: '/home/Pictures', name: 'Pictures' }

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
  ['list', FileList],
  ['grid', FileGrid]
] as const)('%s file activation', (_name, View) => {
  function renderView(initialItems: FileEntry[] = [documents, pictures]) {
    const opened: { path: string; selected: string[] }[] = []
    const onOpen = vi.fn((item: FileEntry) => {
      opened.push({ path: item.id, selected: [...useSelectionStore.getState().names] })
      // Navigation clears selection. No later pointer event may restore the old entry.
      selection.clear()
    })
    const onContextMenu = vi.fn()
    const requestMore = vi.fn()
    const props = { loading: false, loadingMore: false, requestMore, onOpen, onContextMenu }
    const result = render(
      <View {...props} items={initialItems} total={initialItems.length} dirs={initialItems.length} />
    )
    return {
      ...result,
      opened,
      onOpen,
      onContextMenu,
      // Rows and cards are the only elements that carry aria-selected.
      entries: () => Array.from(result.container.querySelectorAll<HTMLElement>('[aria-selected]')),
      async replaceEntries(items: FileEntry[]) {
        await act(async () => {})
        result.rerender(<View {...props} items={items} total={items.length} dirs={items.length} />)
        await act(async () => {})
      }
    }
  }

  it('keeps mouse single-click selection and double-click open', () => {
    const view = renderView()
    const [row] = view.entries()

    tap(row, { pointerType: 'mouse', detail: 1 })
    expect(row.getAttribute('aria-selected')).toBe('true')
    expect(view.opened).toEqual([])

    tap(row, { pointerType: 'mouse', detail: 2 })
    expect(view.opened).toEqual([{ path: documents.id, selected: [documents.name] }])
    fireEvent.doubleClick(row)
    expect(view.onOpen).toHaveBeenCalledTimes(1)
    expect([...useSelectionStore.getState().names]).toEqual([])
  })

  it('opens on the first completed touch tap without selecting the row or card', () => {
    const view = renderView()
    const [row] = view.entries()

    tap(row, { pointerType: 'touch' })

    expect(view.opened).toEqual([{ path: documents.id, selected: [] }])
    expect(view.onOpen).toHaveBeenCalledTimes(1)
    expect(row.getAttribute('aria-selected')).toBe('false')
    expect([...useSelectionStore.getState().names]).toEqual([])
  })

  it('opens each touched path independently, including repeated names after navigation', async () => {
    const view = renderView([documents])

    tap(view.entries()[0], { pointerType: 'touch' })
    expect(view.opened.map((entry) => entry.path)).toEqual([documents.id])

    const nested = { ...documents, id: '/home/Documents/Documents' }
    await view.replaceEntries([nested])
    tap(view.entries()[0], { pointerType: 'touch', pointerId: 2 })
    expect(view.opened.map((entry) => entry.path)).toEqual([documents.id, nested.id])

    const deeper = { ...documents, id: `${nested.id}/Documents` }
    await view.replaceEntries([deeper])
    tap(view.entries()[0], { pointerType: 'touch', pointerId: 3 })
    expect(view.opened.map((entry) => entry.path)).toEqual([documents.id, nested.id, deeper.id])
  })

  it.each(['cancelled', 'moved', 'held'] as const)(
    'does not open after a %s touch gesture, then accepts the next tap',
    (gesture) => {
      const view = renderView()
      const [row] = view.entries()

      pointer(row, 'pointerdown', { pointerType: 'touch' })
      if (gesture === 'cancelled') pointer(row, 'pointercancel', { pointerType: 'touch' })
      if (gesture === 'moved') pointer(row, 'pointermove', { pointerType: 'touch', clientX: 60 })
      if (gesture === 'held') vi.advanceTimersByTime(500)
      pointer(row, 'pointerup', { pointerType: 'touch' })
      pointer(row, 'click', { pointerType: 'touch', detail: 1 })
      expect(view.opened).toEqual([])

      tap(row, { pointerType: 'touch', pointerId: 2 })
      expect(view.opened.map((entry) => entry.path)).toEqual([documents.id])
    }
  )

  it('keeps selection and more-actions independent from touch activation', () => {
    const view = renderView()
    const [row] = view.entries()
    const check = within(row).getByText(`Select ${documents.name}`)
    const menu = within(row).getByRole('button', { name: /^More/ })

    tap(check, { pointerType: 'touch' })
    expect(row.getAttribute('aria-selected')).toBe('true')
    expect(view.opened).toEqual([])

    tap(menu, { pointerType: 'touch', pointerId: 2 })
    expect(view.onContextMenu).toHaveBeenCalledTimes(1)
    expect(view.opened).toEqual([])
    expect(row.getAttribute('aria-selected')).toBe('true')

    tap(row, { pointerType: 'touch', pointerId: 3 })
    expect(view.opened).toEqual([{ path: documents.id, selected: [documents.name] }])
  })

  it('preserves desktop modifier selection without opening', () => {
    const view = renderView()
    const [a, b] = view.entries()

    tap(a, { pointerType: 'mouse', detail: 1 })
    tap(b, { pointerType: 'mouse', detail: 2, ctrlKey: true })
    fireEvent.doubleClick(b, { ctrlKey: true })

    expect([...useSelectionStore.getState().names]).toEqual([documents.name, pictures.name])
    expect(view.opened).toEqual([])
  })

  it('does not treat a touch with keyboard modifiers as activation', () => {
    const view = renderView()
    const [row] = view.entries()

    tap(row, { pointerType: 'touch', ctrlKey: true })

    expect(view.opened).toEqual([])
    expect(row.getAttribute('aria-selected')).toBe('false')
  })
})
