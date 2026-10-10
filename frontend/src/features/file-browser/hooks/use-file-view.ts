import { useCallback, useEffect, useImperativeHandle, useLayoutEffect, useMemo, useRef, useState } from 'react'
import type { KeyboardEvent, MouseEvent, PointerEvent, Ref, RefObject } from 'react'
import { selection, useSelectionStore } from '../model/selection'
import type { FileEntry } from '../model/file-entry'
import type { Rect } from '../logic/marquee'
import type { RowMenuAnchor } from '../logic/row-actions'
import { useFileActivation } from './use-file-activation'
import { useFileFocusPreservation } from './use-file-focus-preservation'

/** What the page reaches into a file view for: deep links, and rubber-band selection with its auto-scroll. */
export interface FileViewHandle {
  /** Selects and reveals the named item. False when it has not loaded yet. */
  focusEntry: (name: string) => boolean
  itemsInRect: (rect: Rect) => FileEntry[]
  scrollBounds: () => { top: number; height: number }
  scrollBy: (delta: number) => void
}

/** What the grid and the list both take. */
export interface FileViewProps {
  items: readonly FileEntry[]
  /** The whole folder, loaded or not. Items past `items` show as placeholders. */
  total: number
  /** Folders sort first, so the first `dirs` of `total` are folders. */
  dirs: number
  loading: boolean
  loadingMore: boolean
  requestMore: () => void
  onOpen: (item: FileEntry) => void
  onContextMenu: (item: FileEntry, anchor: RowMenuAnchor) => void
  onRename?: () => void
  onDelete?: () => void
  onSearchFocus?: () => void
  encrypted?: boolean
  onNavigateParent?: () => void
}

/** Where a view draws its items, worked out each render from its own measurements. */
export interface FileViewLayout {
  /** The index an arrow key moves focus to, or null for a key this layout does not move on. */
  step: (key: string, from: number | null) => number | null
  /** Scrolls just far enough to show the item at `index`. */
  reveal: (index: number) => void
  indicesInRect: (rect: Rect) => number[]
  /** Whether the item at `index` is in the rendered window. */
  rendered: (index: number) => boolean
  /** One past the last index the window draws. Pages load until the listing reaches it. */
  needed: number
}

const sameNumbers = <M extends Record<string, number>>(a: M, b: M): boolean =>
  Object.keys(a).every((key) => a[key] === b[key])

/**
 * Reads `measure` from the scrolling element on resize and after every render, since what the view renders moves
 * its own sections. Scrolling is left to the virtualizers. `measure` must be stable. An unchanged reading does not
 * re-render.
 */
export function useViewportMetrics<M extends Record<string, number>>(
  measure: (element: HTMLDivElement) => M,
  initial: M
): { viewport: RefObject<HTMLDivElement | null>; metrics: M } {
  const viewport = useRef<HTMLDivElement>(null)
  const [metrics, setMetrics] = useState(initial)
  const update = useCallback(() => {
    if (!viewport.current) return
    const next = measure(viewport.current)
    setMetrics((previous) => (sameNumbers(previous, next) ? previous : next))
  }, [measure])
  useLayoutEffect(update)
  useEffect(() => {
    const element = viewport.current
    if (!element || typeof ResizeObserver === 'undefined') return
    const observer = new ResizeObserver(update)
    observer.observe(element)
    return () => observer.disconnect()
  }, [update])
  return { viewport, metrics }
}

/**
 * The behavior the grid and the list share: selection, keyboard navigation, pointer activation, paging and the
 * handle. The view supplies the geometry; `idPrefix` keeps the two views' element ids apart.
 */
export function useFileView(
  props: FileViewProps,
  ref: Ref<FileViewHandle>,
  viewport: RefObject<HTMLDivElement | null>,
  layout: FileViewLayout,
  idPrefix: string
) {
  const { items, total, loadingMore, requestMore, onOpen, onContextMenu } = props
  const names = useSelectionStore((state) => state.names)
  const focused = useSelectionStore((state) => state.focused)
  const focusedName = useFileFocusPreservation(items, focused)
  const loadedNames = useMemo(() => items.map((item) => item.name), [items])
  const domId = (name: string) => `${idPrefix}-${encodeURIComponent(name).replace(/%/g, '_')}`

  const { needed } = layout
  const loaded = items.length
  useEffect(() => {
    if (!loadingMore && needed > loaded) requestMore()
  }, [loadingMore, needed, loaded, requestMore])

  const activation = useFileActivation((item, index, event) => {
    viewport.current?.focus()
    if (event.shiftKey) selection.range(loadedNames, item.name)
    else if (event.ctrlKey || event.metaKey) selection.toggle(item.name, index)
    else selection.only(item.name, index)
  }, onOpen)

  useImperativeHandle(ref, () => ({
    focusEntry(name) {
      const index = items.findIndex((item) => item.name === name)
      if (index < 0) return false
      selection.only(name, index)
      viewport.current?.focus()
      layout.reveal(index)
      return true
    },
    itemsInRect: (rect) => layout.indicesInRect(rect).flatMap((index) => items[index] ?? []),
    scrollBounds() {
      if (!viewport.current) return { top: 0, height: window.innerHeight }
      const box = viewport.current.getBoundingClientRect()
      return { top: box.top, height: box.height }
    },
    scrollBy(delta) {
      if (viewport.current) viewport.current.scrollTop += delta
    }
  }))

  // The keyboard's menu opens at the middle of the focused item, as a pointer's would at the pointer.
  const openMenuAt = (item: FileEntry) => {
    const element = document.getElementById(domId(item.name))
    if (!element) return
    const box = element.getBoundingClientRect()
    onContextMenu(item, {
      clientX: Math.round(box.left + box.width / 2),
      clientY: Math.round(box.top + box.height / 2),
      currentTarget: element
    })
  }

  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    activation.cancel()
    const target = total > 0 ? layout.step(event.key, focused) : null
    if (target !== null) {
      event.preventDefault()
      const next = Math.min(Math.max(target, 0), total - 1)
      selection.focus(next)
      const name = items[next]?.name
      if (event.shiftKey && name) selection.range(loadedNames, name)
      layout.reveal(next)
      return
    }
    const item = focused === null ? undefined : items[focused]
    if (event.key === ' ') {
      event.preventDefault()
      if (item && focused !== null) selection.toggle(item.name, focused)
    } else if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'a') {
      event.preventDefault()
      selection.all(loadedNames)
    } else if (event.key === 'Enter') {
      if (item) onOpen(item)
    } else if (event.key === 'ContextMenu' || (event.key === 'F10' && event.shiftKey)) {
      event.preventDefault()
      if (item) openMenuAt(item)
    } else if (event.key === 'F2') {
      event.preventDefault()
      props.onRename?.()
    } else if (event.key === 'Delete') {
      event.preventDefault()
      props.onDelete?.()
    } else if (event.key === '/') {
      event.preventDefault()
      props.onSearchFocus?.()
    } else if (event.key === 'Escape' && names.size) {
      event.preventDefault()
      selection.clear()
    }
  }

  const active = focused !== null && items[focused] && layout.rendered(focused) ? domId(items[focused].name) : undefined

  return {
    names,
    /** The props of the scrolling element, which is also the grid the keyboard works in. */
    rootProps: {
      ref: viewport,
      role: 'grid',
      'aria-multiselectable': true,
      'aria-activedescendant': active,
      'aria-busy': loadingMore,
      tabIndex: 0,
      onKeyDown,
      onPointerDown: (event: PointerEvent<HTMLDivElement>) => {
        if (!(event.target as HTMLElement).closest('[aria-selected]')) activation.cancel()
      },
      onContextMenu: activation.cancel
    },
    /** What one item needs from the view, apart from where it sits. */
    itemProps: (item: FileEntry, index: number) => ({
      item,
      id: domId(item.name),
      selected: names.has(item.name),
      focused: focusedName === item.name,
      encrypted: props.encrypted,
      ...activation.handlers(item, index),
      onContextMenu: (event: MouseEvent<HTMLElement>) => {
        activation.cancel()
        event.preventDefault()
        event.stopPropagation()
        onContextMenu(item, event)
      },
      onMenu: (event: MouseEvent<HTMLElement>) => onContextMenu(item, event),
      onToggle: () => selection.toggle(item.name, index),
      onCancelActivation: activation.cancel
    })
  }
}
