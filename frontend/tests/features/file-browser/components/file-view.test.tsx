import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createRef } from 'react'
import type { FileEntry } from '../../../../src/features/file-browser/model/file-entry'
import { selection, useSelectionStore } from '../../../../src/features/file-browser/model/selection'
import { act, cleanup, fireEvent, render, within } from '../../../../src/test/test-utils'
import { FileGrid } from '../../../../src/features/file-browser/components/FileGrid'
import { FileList } from '../../../../src/features/file-browser/components/FileList'
import type { FileViewHandle } from '../../../../src/features/file-browser/hooks/use-file-view'
import { density } from '../../../../src/features/file-browser/model/view-prefs'

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
  // jsdom has no layout, and the virtualizers draw nothing in a viewport with no height.
  vi.spyOn(HTMLElement.prototype, 'offsetHeight', 'get').mockReturnValue(600)
  vi.spyOn(HTMLElement.prototype, 'offsetWidth', 'get').mockReturnValue(800)
  vi.spyOn(HTMLElement.prototype, 'clientHeight', 'get').mockReturnValue(600)
  vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockReturnValue(800)
  vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockReturnValue(new DOMRect(0, 0, 800, 600))
  selection.reset()
  density.value = 'comfortable'
  vi.useFakeTimers()
  vi.setSystemTime(new Date('2026-01-01T00:00:00Z'))
})
afterEach(async () => {
  await act(async () => {})
  cleanup()
  selection.reset()
  density.value = 'comfortable'
  vi.useRealTimers()
  vi.restoreAllMocks()
})

describe.each([
  ['list', FileList],
  ['grid', FileGrid]
] as const)('%s file activation', (_name, View) => {
  function renderView(initialItems: FileEntry[] = [documents, pictures], withParent = false) {
    const ref = createRef<FileViewHandle>()
    const onNavigateParent = withParent ? vi.fn() : undefined
    const opened: { path: string; selected: string[] }[] = []
    const onOpen = vi.fn((item: FileEntry) => {
      opened.push({ path: item.id, selected: [...useSelectionStore.getState().names] })
      // Navigation clears selection. No later pointer event may restore the old entry.
      selection.clear()
    })
    const onContextMenu = vi.fn()
    const requestMore = vi.fn()
    const props = { loading: false, loadingMore: false, requestMore, onOpen, onContextMenu, onNavigateParent }
    const result = render(
      <View
        ref={ref}
        {...props}
        items={initialItems}
        total={initialItems.length}
        dirs={initialItems.filter((item) => item.type === 'folder').length}
      />
    )
    return {
      ...result,
      opened,
      onOpen,
      onContextMenu,
      onNavigateParent,
      ref,
      // Rows and cards are the only elements that carry aria-selected.
      entries: () => Array.from(result.container.querySelectorAll<HTMLElement>('[aria-selected]')),
      async replaceEntries(items: FileEntry[]) {
        await act(async () => {})
        result.rerender(
          <View
            ref={ref}
            {...props}
            items={items}
            total={items.length}
            dirs={items.filter((item) => item.type === 'folder').length}
          />
        )
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

  it.each(['compact', 'comfortable', 'spacious'] as const)(
    'matches folder dimensions and styles at %s density without selection or menu controls',
    (value) => {
      density.value = value
      const view = renderView(undefined, true)
      const button = view.getByRole('button', { name: 'Go to parent folder' })
      const parent = button.closest<HTMLElement>(_name === 'list' ? '[role="row"]' : '[role="gridcell"]')!
      const [folder] = view.entries()

      expect(parent.getAttribute('style')).toBe(folder.getAttribute('style'))
      for (const className of folder.classList) expect(parent.classList.contains(className)).toBe(true)
      expect(parent.querySelector('svg[width]')?.getAttribute('width')).toBe(
        folder.querySelector('svg[width]')?.getAttribute('width')
      )
      expect(parent.hasAttribute('aria-selected')).toBe(false)
      expect(within(parent).queryByRole('checkbox')).toBeNull()
      expect(within(parent).getAllByRole('button')).toEqual([button])
      expect(parent.compareDocumentPosition(folder) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0)
      if (_name === 'list') {
        expect(parent.getAttribute('aria-rowindex')).toBe('2')
        expect(folder.getAttribute('aria-rowindex')).toBe('3')
        expect(within(parent).getAllByRole('gridcell')).toHaveLength(5)
      } else {
        expect(parent.getAttribute('aria-colindex')).toBe('1')
        expect(folder.getAttribute('aria-colindex')).toBe('2')
      }
    }
  )

  it('navigates directly without selecting the parent or changing file selection', () => {
    const view = renderView(undefined, true)
    selection.only(documents.name, 0)
    const button = view.getByRole('button', { name: 'Go to parent folder' })

    fireEvent.click(button)
    expect(view.onNavigateParent).toHaveBeenCalledTimes(1)
    expect(view.onOpen).not.toHaveBeenCalled()
    expect([...useSelectionStore.getState().names]).toEqual([documents.name])

    fireEvent.keyDown(button, { key: 'Enter' })
    fireEvent.keyDown(button, { key: ' ', ctrlKey: true })
    fireEvent.keyDown(button, { key: 'ContextMenu' })
    expect(view.onOpen).not.toHaveBeenCalled()
    expect(view.onContextMenu).not.toHaveBeenCalled()
    expect([...useSelectionStore.getState().names]).toEqual([documents.name])
  })

  it('excludes parent navigation from select-all and marquee hit testing', () => {
    const view = renderView(undefined, true)
    fireEvent.keyDown(view.getByRole('grid'), { key: 'a', ctrlKey: true })
    expect([...useSelectionStore.getState().names]).toEqual([documents.name, pictures.name])
    expect(view.ref.current?.itemsInRect({ left: 0, top: 0, right: 224, bottom: _name === 'list' ? 88 : 52 })).toEqual(
      []
    )
    expect(view.ref.current?.itemsInRect({ left: 0, top: 0, right: 1000, bottom: 1000 })).toEqual([documents, pictures])
  })

  it('keeps file indices intact when parent navigation wraps the folder section', () => {
    const folders = Array.from({ length: 3 }, (_, index) => ({
      ...documents,
      id: `/home/folder-${index}`,
      name: `folder-${index}`
    }))
    const files = Array.from({ length: 3 }, (_, index) => ({
      ...documents,
      id: `/home/file-${index}.txt`,
      name: `file-${index}.txt`,
      type: 'file' as const
    }))
    const view = renderView([...folders, ...files], true)
    expect(view.entries().map((entry) => entry.querySelector('[title]')?.getAttribute('title'))).toEqual(
      [...folders, ...files].map((item) => item.name)
    )
    const grid = view.getByRole('grid')
    fireEvent.keyDown(grid, { key: 'ArrowDown' })
    fireEvent.keyDown(grid, { key: 'ArrowDown' })
    const focused = _name === 'grid' ? 2 : 1
    expect(useSelectionStore.getState().focused).toBe(focused)
    if (_name === 'grid') {
      fireEvent.keyDown(grid, { key: 'ArrowDown' })
      expect(useSelectionStore.getState().focused).toBe(3)
      fireEvent.keyDown(grid, { key: 'ArrowUp' })
      expect(useSelectionStore.getState().focused).toBe(2)
    }
    fireEvent.keyDown(grid, { key: 'Enter' })
    expect(view.opened[0].path).toBe(folders[focused].id)
    expect(view.ref.current?.itemsInRect({ left: 0, top: 0, right: 1000, bottom: 1000 })).toEqual([
      ...folders,
      ...files
    ])
  })

  it('keeps files in their own section when the only folder card is parent navigation', () => {
    const file = { ...documents, id: '/home/report.txt', name: 'report.txt', type: 'file' as const }
    const view = renderView([file], true)
    expect(view.entries()).toHaveLength(1)
    expect(view.entries()[0].querySelector('[title]')?.getAttribute('title')).toBe(file.name)
    if (_name === 'grid') {
      expect(within(view.getByRole('rowgroup', { name: 'Folders' })).queryByText(file.name)).toBeNull()
      expect(within(view.getByRole('rowgroup', { name: 'Files' })).getByTitle(file.name)).toBeTruthy()
    }
    fireEvent.keyDown(view.getByRole('grid'), { key: 'ArrowDown' })
    fireEvent.keyDown(view.getByRole('grid'), { key: 'ArrowUp' })
    expect(useSelectionStore.getState().focused).toBe(0)
    fireEvent.keyDown(view.getByRole('grid'), { key: 'Enter' })
    expect(view.opened[0].path).toBe(file.id)
  })

  it('keeps parent navigation available alongside the empty-folder message', () => {
    const view = renderView([], true)
    expect(view.getByText('This folder is empty')).toBeTruthy()
    fireEvent.click(view.getByRole('button', { name: 'Go to parent folder' }))
    expect(view.onNavigateParent).toHaveBeenCalledTimes(1)
    expect(view.entries()).toEqual([])
  })
})
