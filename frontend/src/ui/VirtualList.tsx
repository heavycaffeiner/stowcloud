import { useCallback, useLayoutEffect, useMemo, useRef, useState } from 'react'
import type { HTMLAttributes, KeyboardEvent, ReactNode } from 'react'
import {
  effectiveViewportHeight,
  measuredRows,
  type RowLayout,
  type WindowResult,
  windowOver
} from '../lib/virtual/windowing'

type ItemKey = string | number

export interface VirtualListProps<T> extends Omit<HTMLAttributes<HTMLUListElement>, 'children'> {
  items: readonly T[]
  itemKey: (item: T, index: number) => ItemKey
  estimateSize: number
  renderItem: (item: T, index: number) => ReactNode
  itemProps?: (item: T, index: number) => HTMLAttributes<HTMLLIElement>
  pinnedKeys?: readonly ItemKey[]
}

const SMALL_LIST_LIMIT = 40
const OVERSCAN = 6
const EMPTY_KEYS: readonly ItemKey[] = []
const NO_ROWS = measuredRows([], 0)

function composedParent(element: Element): Element | null {
  return (
    element.assignedSlot ??
    element.parentElement ??
    (element.getRootNode() instanceof ShadowRoot ? (element.getRootNode() as ShadowRoot).host : null)
  )
}

function documentScroller(element: HTMLElement): boolean {
  return element === (element.ownerDocument.scrollingElement ?? element.ownerDocument.documentElement)
}

function scrollOffset(element: HTMLElement): number {
  return documentScroller(element) ? (element.ownerDocument.defaultView?.scrollY ?? 0) : element.scrollTop
}

// A growing overflow:auto wrapper is not a scroll owner. Follow slots as well as
// parents so a light-DOM list can use the scrolling body inside an MDUI dialog.
function scrollOwner(list: HTMLUListElement): HTMLElement {
  for (let element: Element | null = list; element; element = composedParent(element)) {
    if (
      element instanceof HTMLElement &&
      !documentScroller(element) &&
      /^(auto|scroll|overlay)$/.test(getComputedStyle(element).overflowY) &&
      element.scrollHeight > element.clientHeight + 1
    )
      return element
  }
  return (list.ownerDocument.scrollingElement ?? list.ownerDocument.documentElement) as HTMLElement
}

interface Viewport {
  readonly offset: number
  readonly height: number
}

function viewportOf(owner: HTMLElement): Viewport {
  if (!documentScroller(owner)) return { offset: owner.scrollTop, height: owner.clientHeight }
  const win = owner.ownerDocument.defaultView
  if (!win) return { offset: 0, height: 0 }
  return { offset: win.scrollY, height: effectiveViewportHeight(win.visualViewport?.height, win.innerHeight) }
}

function scrollOwnerTo(owner: HTMLElement, top: number): void {
  if (Math.abs(scrollOffset(owner) - top) < 1) return
  if (documentScroller(owner)) owner.ownerDocument.defaultView?.scrollTo({ top })
  else owner.scrollTop = top
}

function observeViewport(owner: HTMLElement, onChange: () => void): () => void {
  const win = owner.ownerDocument.defaultView
  if (!win) return () => undefined
  if (!documentScroller(owner)) {
    const observer = new ResizeObserver(onChange)
    observer.observe(owner)
    owner.addEventListener('scroll', onChange, { passive: true })
    return () => {
      observer.disconnect()
      owner.removeEventListener('scroll', onChange)
    }
  }
  win.addEventListener('scroll', onChange, { passive: true })
  win.addEventListener('resize', onChange, { passive: true })
  win.visualViewport?.addEventListener('resize', onChange, { passive: true })
  return () => {
    win.removeEventListener('scroll', onChange)
    win.removeEventListener('resize', onChange)
    win.visualViewport?.removeEventListener('resize', onChange)
  }
}

/** The indices to mount: the window, plus retained rows outside it, in document order. */
function mountedIndices(win: WindowResult, retained: ReadonlySet<number>): number[] {
  const indices: number[] = []
  for (let index = win.start; index < win.end; index++) indices.push(index)
  for (const index of retained) if (index >= 0 && (index < win.start || index >= win.end)) indices.push(index)
  return indices.sort((a, b) => a - b)
}

/** Top of a row inside the list's padding box. Rows outside the window keep their place on the compressed scale. */
function rowTopIn(rows: RowLayout, win: WindowResult, index: number): number {
  if (index >= win.start && index < win.end) return win.padTop + rows.top(index) - rows.top(win.start)
  return win.scaled ? (rows.top(index) * win.totalHeight) / rows.height : rows.top(index)
}

function activeElement(document: Document): Element | null {
  let active = document.activeElement
  while (active?.shadowRoot?.activeElement) active = active.shadowRoot.activeElement
  return active
}

function ownRow(list: HTMLUListElement, target: Element | null): HTMLLIElement | null {
  for (let element = target; element && element !== list; element = composedParent(element)) {
    if (element.parentElement === list && element instanceof HTMLLIElement) return element
  }
  return null
}

function nearestList(target: Element): Element | null {
  for (let element: Element | null = target; element; element = composedParent(element)) {
    if (element.hasAttribute('data-sc-virtual-list')) return element
  }
  return null
}

// Traverse only one mounted row, including custom-element controls and slots.
// An element with a tab stop owns its shadow focus scope (e.g. mdui-checkbox).
function tabStops(row: HTMLElement): HTMLElement[] {
  const result: HTMLElement[] = []
  const visit = (element: Element) => {
    if (
      !(element instanceof HTMLElement) ||
      element.hidden ||
      element.hasAttribute('inert') ||
      element.matches(':disabled, [disabled]') ||
      getComputedStyle(element).visibility === 'hidden' ||
      element.getClientRects().length === 0
    )
      return
    if (element.tabIndex >= 0) {
      result.push(element)
      if (element.shadowRoot) return
    }
    const children =
      element instanceof HTMLSlotElement
        ? element.assignedElements({ flatten: true })
        : element.shadowRoot
          ? Array.from(element.shadowRoot.children)
          : Array.from(element.children)
    for (const child of children) visit(child)
  }
  visit(row)
  return result
}

function focusedWithin(stop: HTMLElement, active: Element | null): boolean {
  for (let element = active; element; element = composedParent(element)) if (element === stop) return true
  return false
}

function positionProps(
  props: HTMLAttributes<HTMLLIElement> | undefined,
  index: number,
  count: number
): HTMLAttributes<HTMLLIElement> {
  const role = props?.role
  if (
    role &&
    !['listitem', 'option', 'treeitem', 'menuitem', 'menuitemcheckbox', 'menuitemradio', 'radio'].includes(role)
  )
    return props ?? {}
  return { 'aria-posinset': index + 1, 'aria-setsize': count, ...props }
}

export function VirtualList<T>({
  items,
  itemKey,
  estimateSize,
  renderItem,
  itemProps,
  pinnedKeys = EMPTY_KEYS,
  style,
  onKeyDown,
  onFocusCapture,
  onBlurCapture,
  tabIndex,
  ...listProps
}: VirtualListProps<T>) {
  // Keep the same ul/li tree across the threshold, so a growing or shrinking
  // list does not remount a focused control. Disabled instances have no scroll
  // subscriptions, row measurements or full-data key indexes.
  const windowed = items.length > SMALL_LIST_LIMIT
  const listRef = useRef<HTMLUListElement>(null)
  const [interaction, setInteraction] = useState({
    layout: {
      owner: null as HTMLElement | null,
      visible: false,
      margin: 0,
      gap: 0,
      top: 0,
      bottom: 0,
      left: 0,
      right: 0,
      borders: 0
    },
    focusedKey: null as ItemKey | null,
    focusRequest: null as { key: ItemKey; backwards: boolean } | null
  })
  const { layout, focusedKey, focusRequest } = interaction
  const keyed = useMemo(() => {
    const keys: ItemKey[] = []
    const indices = new Map<ItemKey, number>()
    if (windowed)
      for (let index = 0; index < items.length; index++) {
        const key = itemKey(items[index], index)
        keys.push(key)
        indices.set(key, index)
      }
    return { keys, indices }
  }, [items, itemKey, windowed])
  const activeWindowing = windowed && layout.visible
  const retained = useMemo(() => {
    const indices = new Set<number>([0, items.length - 1])
    for (const key of pinnedKeys) {
      const index = keyed.indices.get(key)
      if (index !== undefined) indices.add(index)
    }
    if (focusedKey !== null) {
      const index = keyed.indices.get(focusedKey)
      if (index !== undefined) indices.add(index)
    }
    if (focusRequest) {
      const index = keyed.indices.get(focusRequest.key)
      if (index !== undefined) indices.add(index)
    }
    return indices
  }, [items.length, keyed, pinnedKeys, focusedKey, focusRequest])
  const [viewport, setViewport] = useState<Viewport>({ offset: 0, height: 0 })
  // Measured heights by item key, so a row keeps its height when the list is sorted or filtered.
  const measured = useRef(new Map<ItemKey, number>())
  const [measuredVersion, setMeasuredVersion] = useState(0)
  const estimate = Math.max(1, estimateSize)
  const rows = useMemo(
    () =>
      activeWindowing
        ? measuredRows(
            keyed.keys.map((key) => measured.current.get(key) ?? estimate),
            layout.gap
          )
        : NO_ROWS,
    // measuredVersion stands in for the contents of the measured map.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [activeWindowing, keyed, estimate, layout.gap, measuredVersion]
  )
  // Owner scroll offset at which the first row's top edge sits.
  const base = layout.margin + layout.top
  const win = windowOver(rows, viewport.offset - base, viewport.height, OVERSCAN)
  const latest = useRef({ owner: layout.owner, keyed, rows, base, win, estimate })
  const pendingShift = useRef(0)
  const rowObserver = useRef<ResizeObserver | null>(null)
  const anchor = useRef<{ keys: readonly ItemKey[]; key: ItemKey; offset: number } | null>(null)

  const captureAnchor = useCallback(() => {
    const { owner, keyed, rows, base, win } = latest.current
    const offset = owner ? scrollOffset(owner) - base : -1
    if (!owner || win.scaled || rows.count === 0 || offset < 0) {
      anchor.current = null
      return
    }
    const index = rows.indexAt(offset)
    anchor.current = { keys: keyed.keys, key: keyed.keys[index], offset: offset - rows.top(index) }
  }, [])

  // Renders again only when the scroll position moves the window.
  const syncViewport = useCallback(() => {
    const { owner, rows, base, win } = latest.current
    if (!owner) return
    const next = viewportOf(owner)
    const moved = windowOver(rows, next.offset - base, next.height, OVERSCAN)
    if (moved.start !== win.start || moved.end !== win.end || moved.padTop !== win.padTop) setViewport(next)
    captureAnchor()
  }, [captureAnchor])

  const measureRow = useCallback((element: HTMLLIElement | null) => {
    if (!element) return
    rowObserver.current ??= new ResizeObserver((entries) => {
      const { owner, keyed, rows, base, estimate } = latest.current
      const offset = owner ? scrollOffset(owner) - base : 0
      let changed = false
      for (const entry of entries) {
        const index = Number((entry.target as HTMLElement).dataset.index)
        const key = keyed.keys[index]
        if (key === undefined) continue
        const box = entry.borderBoxSize[0]
        const size = Math.round(box ? box.blockSize : entry.target.getBoundingClientRect().height)
        const previous = measured.current.get(key) ?? estimate
        if (size === previous) continue
        measured.current.set(key, size)
        changed = true
        // A row above the viewport that changes height would push the visible rows; scroll with it instead.
        if (index < rows.count && rows.top(index) < offset) pendingShift.current += size - previous
      }
      if (changed) setMeasuredVersion((version) => version + 1)
    })
    const observer = rowObserver.current
    observer.observe(element)
    return () => observer.unobserve(element)
  }, [])

  useLayoutEffect(() => {
    const list = listRef.current
    if (!list || !windowed) return
    let frame = 0
    const update = () => {
      frame = 0
      const owner = scrollOwner(list)
      const css = getComputedStyle(list)
      const px = (value: string) => Number.parseFloat(value) || 0
      const margin =
        owner === list
          ? 0
          : list.getBoundingClientRect().top +
            list.clientTop +
            scrollOffset(owner) -
            (documentScroller(owner) ? 0 : owner.getBoundingClientRect().top + owner.clientTop)
      const next = {
        owner,
        visible: list.getClientRects().length > 0,
        margin,
        gap: px(css.rowGap),
        top: px(css.paddingTop),
        bottom: px(css.paddingBottom),
        left: px(css.paddingLeft),
        right: px(css.paddingRight),
        borders: px(css.borderTopWidth) + px(css.borderBottomWidth)
      }
      setInteraction((previous) => ({
        ...previous,
        layout: Object.keys(next).every(
          (key) => previous.layout[key as keyof typeof next] === next[key as keyof typeof next]
        )
          ? previous.layout
          : next
      }))
    }
    const schedule = () => {
      if (!frame) frame = requestAnimationFrame(update)
    }
    const observer = new ResizeObserver(schedule)
    const mutations = new MutationObserver(schedule)
    for (let element: Element | null = list; element; element = composedParent(element)) {
      observer.observe(element)
      for (let sibling = element.previousElementSibling; sibling; sibling = sibling.previousElementSibling)
        observer.observe(sibling)
      // Do not observe row descendants: their own measurement observers handle
      // updates without rescanning ancestry or every data record on scroll.
      if (element !== list)
        mutations.observe(element, { attributes: true, attributeFilter: ['class', 'style', 'open'], childList: true })
      element.addEventListener('slotchange', schedule)
    }
    const win = list.ownerDocument.defaultView!
    win.addEventListener('resize', schedule, { passive: true })
    update()
    return () => {
      cancelAnimationFrame(frame)
      observer.disconnect()
      mutations.disconnect()
      for (let element: Element | null = list; element; element = composedParent(element))
        element.removeEventListener('slotchange', schedule)
      win.removeEventListener('resize', schedule)
    }
  }, [windowed, style, listProps.className])

  useLayoutEffect(() => {
    const owner = layout.owner
    if (!owner || !activeWindowing) return
    return observeViewport(owner, syncViewport)
  }, [layout.owner, activeWindowing, syncViewport])

  useLayoutEffect(
    () => () => {
      rowObserver.current?.disconnect()
      rowObserver.current = null
    },
    []
  )

  // Key-based anchoring handles insertions, deletions and sorting without
  // changing the reading position. The key index is rebuilt only with data.
  useLayoutEffect(() => {
    latest.current = { owner: layout.owner, keyed, rows, base, win, estimate }
    const shift = pendingShift.current
    pendingShift.current = 0
    if (!activeWindowing || !layout.owner) {
      anchor.current = null
      return
    }
    const previous = anchor.current
    const index = previous && previous.keys !== keyed.keys ? keyed.indices.get(previous.key) : undefined
    if (previous && index !== undefined) scrollOwnerTo(layout.owner, base + rows.top(index) + previous.offset)
    else if (shift !== 0) scrollOwnerTo(layout.owner, scrollOffset(layout.owner) + shift)
    syncViewport()
  })

  useLayoutEffect(() => {
    if (!focusRequest || !listRef.current) return
    const index = keyed.indices.get(focusRequest.key)
    if (index === undefined) {
      setInteraction((previous) => ({ ...previous, focusRequest: null }))
      return
    }
    const row = listRef.current.querySelector<HTMLLIElement>(`:scope > li[data-index="${index}"]`)
    if (!row) return
    const stops = tabStops(row)
    const target = (focusRequest.backwards ? stops.at(-1) : stops[0]) ?? row
    target.focus({ preventScroll: true })
    if (layout.owner && activeWindowing) {
      const { offset, height } = viewportOf(layout.owner)
      const top = layout.margin + layout.top + rowTopIn(rows, win, index)
      const bottom = top + rows.size(index)
      if (top < offset) scrollOwnerTo(layout.owner, top)
      else if (bottom > offset + height) scrollOwnerTo(layout.owner, bottom - height)
    }
    setInteraction((previous) => ({ ...previous, focusRequest: null }))
  })

  const requestFocus = (index: number, backwards: boolean) => {
    if (index >= 0 && index < items.length)
      setInteraction((previous) => ({ ...previous, focusRequest: { key: keyed.keys[index], backwards } }))
  }
  const handleKeyDown = (event: KeyboardEvent<HTMLUListElement>) => {
    onKeyDown?.(event)
    if (
      !windowed ||
      event.defaultPrevented ||
      event.altKey ||
      event.ctrlKey ||
      event.metaKey ||
      !(event.target instanceof Element) ||
      nearestList(event.target) !== listRef.current
    )
      return
    const list = listRef.current!
    const row = ownRow(list, event.target)
    const index = row ? Number(row.dataset.index) : -1
    if (event.key === 'Tab') {
      // Composite widgets own their roving tab stop and must allow Tab to exit.
      if (
        listProps.role &&
        ['tree', 'listbox', 'menu', 'menubar', 'radiogroup', 'tablist', 'grid'].includes(listProps.role)
      )
        return
      const backwards = event.shiftKey
      if (!row && (backwards || event.target !== list)) return
      if (row) {
        const stops = tabStops(row)
        const current = stops.findIndex((stop) => focusedWithin(stop, activeElement(list.ownerDocument)))
        if (backwards ? current > 0 : current >= 0 && current < stops.length - 1) return
      }
      const next = index + (backwards ? -1 : 1)
      if (next < 0 || next >= items.length) return
      event.preventDefault()
      requestFocus(next, backwards)
    } else if (event.target === row || event.target === list) {
      const next =
        event.key === 'ArrowDown'
          ? index + 1
          : event.key === 'ArrowUp'
            ? index - 1
            : event.key === 'Home'
              ? 0
              : event.key === 'End'
                ? items.length - 1
                : null
      if (next !== null) {
        event.preventDefault()
        requestFocus(next, false)
      }
    }
  }
  const mounted = activeWindowing
    ? mountedIndices(win, retained).map((index) => ({ index, key: keyed.keys[index] }))
    : windowed
      ? []
      : items.map((item, index) => ({ index, key: itemKey(item, index) }))

  return (
    <ul
      {...listProps}
      ref={listRef}
      data-sc-virtual-list=""
      tabIndex={tabIndex ?? (activeWindowing && !listProps.role ? 0 : undefined)}
      style={
        activeWindowing
          ? {
              ...style,
              position: 'relative',
              boxSizing: 'border-box',
              height: layout.top + win.totalHeight + layout.bottom + layout.borders,
              flexShrink: 0,
              overflowAnchor: 'none'
            }
          : style
      }
      onKeyDown={handleKeyDown}
      onFocusCapture={(event) => {
        onFocusCapture?.(event)
        const row = ownRow(event.currentTarget, event.target)
        if (row) {
          const index = Number(row.dataset.index)
          setInteraction((previous) => ({ ...previous, focusedKey: itemKey(items[index], index) }))
        }
      }}
      onBlurCapture={(event) => {
        onBlurCapture?.(event)
        const list = event.currentTarget
        queueMicrotask(() => {
          if (listRef.current !== list) return
          const row = ownRow(list, activeElement(list.ownerDocument))
          const index = row ? Number(row.dataset.index) : -1
          setInteraction((previous) => ({
            ...previous,
            focusedKey: index >= 0 && index < items.length ? itemKey(items[index], index) : null
          }))
        })
      }}
    >
      {mounted.map((row) => {
        const item = items[row.index]
        const suppliedProps = itemProps?.(item, row.index)
        const props = activeWindowing ? positionProps(suppliedProps, row.index, items.length) : (suppliedProps ?? {})
        return (
          <li
            {...props}
            key={row.key}
            ref={activeWindowing ? measureRow : undefined}
            data-index={row.index}
            tabIndex={props.tabIndex ?? (activeWindowing ? -1 : undefined)}
            style={
              activeWindowing
                ? {
                    ...props.style,
                    position: 'absolute',
                    top: layout.top + rowTopIn(rows, win, row.index),
                    left: layout.left,
                    right: layout.right,
                    width: 'auto'
                  }
                : props.style
            }
          >
            {renderItem(item, row.index)}
          </li>
        )
      })}
    </ul>
  )
}
