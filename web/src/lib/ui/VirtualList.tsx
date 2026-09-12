import { useCallback, useLayoutEffect, useMemo, useRef, useState } from 'react'
import type { HTMLAttributes, KeyboardEvent, ReactNode } from 'react'
import { defaultRangeExtractor, elementScroll, observeElementOffset, observeElementRect, useVirtualizer } from '@tanstack/react-virtual'
import type { Range, Rect, Virtualizer } from '@tanstack/react-virtual'

type ItemKey = string | number
type ListVirtualizer = Virtualizer<HTMLElement, HTMLLIElement>

export interface VirtualListProps<T> extends Omit<HTMLAttributes<HTMLUListElement>, 'children'> {
  items: readonly T[]
  itemKey: (item: T, index: number) => ItemKey
  estimateSize: number
  renderItem: (item: T, index: number) => ReactNode
  itemProps?: (item: T, index: number) => HTMLAttributes<HTMLLIElement>
  pinnedKeys?: readonly ItemKey[]
}

const SMALL_LIST_LIMIT = 40
const EMPTY_KEYS: readonly ItemKey[] = []

function composedParent(element: Element): Element | null {
  return element.assignedSlot ?? element.parentElement ?? (element.getRootNode() instanceof ShadowRoot ? (element.getRootNode() as ShadowRoot).host : null)
}

function documentScroller(element: HTMLElement): boolean {
  return element === (element.ownerDocument.scrollingElement ?? element.ownerDocument.documentElement)
}

function scrollOffset(element: HTMLElement): number {
  return documentScroller(element) ? element.ownerDocument.defaultView?.scrollY ?? 0 : element.scrollTop
}

// A growing overflow:auto wrapper is not a scroll owner. Follow slots as well as
// parents so a light-DOM list can use the scrolling body inside an MDUI dialog.
function scrollOwner(list: HTMLUListElement): HTMLElement {
  for (let element: Element | null = list; element; element = composedParent(element)) {
    if (element instanceof HTMLElement && !documentScroller(element) && /^(auto|scroll|overlay)$/.test(getComputedStyle(element).overflowY) && element.scrollHeight > element.clientHeight + 1) return element
  }
  return (list.ownerDocument.scrollingElement ?? list.ownerDocument.documentElement) as HTMLElement
}

function observeRect(instance: ListVirtualizer, callback: (rect: Rect) => void): (() => void) | void {
  const element = instance.scrollElement
  if (!element || !documentScroller(element)) return observeElementRect(instance, callback)
  const win = element.ownerDocument.defaultView
  if (!win) return
  const update = () => callback({ width: element.clientWidth, height: win.visualViewport?.height ?? win.innerHeight })
  update()
  win.addEventListener('resize', update, { passive: true })
  win.visualViewport?.addEventListener('resize', update, { passive: true })
  return () => {
    win.removeEventListener('resize', update)
    win.visualViewport?.removeEventListener('resize', update)
  }
}

function observeOffset(instance: ListVirtualizer, callback: (offset: number, scrolling: boolean) => void): (() => void) | void {
  const element = instance.scrollElement
  if (!element) return
  if (!documentScroller(element)) {
    callback(element.scrollTop, false)
    return observeElementOffset(instance, callback)
  }
  const win = element.ownerDocument.defaultView
  if (!win) return
  let timer: number | undefined
  const stop = () => {
    win.clearTimeout(timer)
    callback(win.scrollY, false)
  }
  const update = () => {
    win.clearTimeout(timer)
    callback(win.scrollY, true)
    timer = win.setTimeout(stop, instance.options.isScrollingResetDelay)
  }
  callback(win.scrollY, false)
  win.addEventListener('scroll', update, { passive: true })
  win.addEventListener('scrollend', stop, { passive: true })
  return () => {
    win.clearTimeout(timer)
    win.removeEventListener('scroll', update)
    win.removeEventListener('scrollend', stop)
  }
}

const scrollTo: ListVirtualizer['options']['scrollToFn'] = (offset, options, instance) => {
  const element = instance.scrollElement
  if (element && documentScroller(element)) element.ownerDocument.defaultView?.scrollTo({ top: offset + (options.adjustments ?? 0), behavior: options.behavior })
  else elementScroll(offset, options, instance)
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
    if (!(element instanceof HTMLElement) || element.hidden || element.hasAttribute('inert') || element.matches(':disabled, [disabled]') || getComputedStyle(element).visibility === 'hidden' || element.getClientRects().length === 0) return
    if (element.tabIndex >= 0) {
      result.push(element)
      if (element.shadowRoot) return
    }
    const children = element instanceof HTMLSlotElement ? element.assignedElements({ flatten: true }) : element.shadowRoot ? Array.from(element.shadowRoot.children) : Array.from(element.children)
    for (const child of children) visit(child)
  }
  visit(row)
  return result
}

function focusedWithin(stop: HTMLElement, active: Element | null): boolean {
  for (let element = active; element; element = composedParent(element)) if (element === stop) return true
  return false
}

function positionProps(props: HTMLAttributes<HTMLLIElement> | undefined, index: number, count: number): HTMLAttributes<HTMLLIElement> {
  const role = props?.role
  if (role && !['listitem', 'option', 'treeitem', 'menuitem', 'menuitemcheckbox', 'menuitemradio', 'radio'].includes(role)) return props ?? {}
  return { 'aria-posinset': index + 1, 'aria-setsize': count, ...props }
}

export function VirtualList<T>({ items, itemKey, estimateSize, renderItem, itemProps, pinnedKeys = EMPTY_KEYS, style, onKeyDown, onFocusCapture, onBlurCapture, tabIndex, ...listProps }: VirtualListProps<T>) {
  // Keep the same ul/li tree across the threshold, so a growing or shrinking
  // list does not remount a focused control. Disabled instances have no scroll
  // subscriptions, row measurements or full-data key indexes.
  const windowed = items.length > SMALL_LIST_LIMIT
  const listRef = useRef<HTMLUListElement>(null)
  const [layout, setLayout] = useState({ owner: null as HTMLElement | null, visible: false, margin: 0, gap: 0, top: 0, bottom: 0, left: 0, right: 0, borders: 0 })
  const [focusedKey, setFocusedKey] = useState<ItemKey | null>(null)
  const [focusRequest, setFocusRequest] = useState<{ key: ItemKey; backwards: boolean } | null>(null)
  const keyed = useMemo(() => {
    const keys: ItemKey[] = []
    const indices = new Map<ItemKey, number>()
    if (windowed) for (let index = 0; index < items.length; index++) {
      const key = itemKey(items[index], index)
      keys.push(key)
      indices.set(key, index)
    }
    return { keys, indices }
  }, [items, itemKey, windowed])
  const activeWindowing = windowed && layout.visible
  const getItemKey = useCallback((index: number) => keyed.keys[index], [keyed])
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
  const rangeExtractor = useCallback((range: Range) => Array.from(new Set([...defaultRangeExtractor(range), ...retained])).sort((a, b) => a - b), [retained])
  const getScrollElement = useCallback(() => layout.owner, [layout.owner])
  const captureAnchor = useRef<(() => void) | null>(null)
  const observeListOffset = useCallback((instance: ListVirtualizer, callback: (offset: number, scrolling: boolean) => void) => observeOffset(instance, (offset, scrolling) => {
    callback(offset, scrolling)
    captureAnchor.current?.()
  }), [])
  const virtualizer = useVirtualizer<HTMLElement, HTMLLIElement>({
    count: activeWindowing ? items.length : 0,
    enabled: activeWindowing,
    getScrollElement,
    getItemKey,
    estimateSize: () => Math.max(1, estimateSize),
    overscan: 6,
    rangeExtractor,
    scrollMargin: layout.margin,
    gap: layout.gap,
    paddingStart: layout.top,
    paddingEnd: layout.bottom,
    observeElementRect: observeRect,
    observeElementOffset: observeListOffset,
    scrollToFn: scrollTo,
    initialOffset: () => layout.owner ? scrollOffset(layout.owner) : 0,
    initialRect: { width: 0, height: 600 },
    useFlushSync: false,
  })
  const anchor = useRef<{ keys: readonly ItemKey[]; key: ItemKey; offset: number } | null>(null)

  useLayoutEffect(() => {
    const list = listRef.current
    if (!list || !windowed) return
    let frame = 0
    const update = () => {
      frame = 0
      const owner = scrollOwner(list)
      const css = getComputedStyle(list)
      const px = (value: string) => Number.parseFloat(value) || 0
      const margin = owner === list ? 0 : list.getBoundingClientRect().top + list.clientTop + scrollOffset(owner) - (documentScroller(owner) ? 0 : owner.getBoundingClientRect().top + owner.clientTop)
      const next = { owner, visible: list.getClientRects().length > 0, margin, gap: px(css.rowGap), top: px(css.paddingTop), bottom: px(css.paddingBottom), left: px(css.paddingLeft), right: px(css.paddingRight), borders: px(css.borderTopWidth) + px(css.borderBottomWidth) }
      setLayout((previous) => Object.keys(next).every((key) => previous[key as keyof typeof next] === next[key as keyof typeof next]) ? previous : next)
    }
    const schedule = () => { if (!frame) frame = requestAnimationFrame(update) }
    const observer = new ResizeObserver(schedule)
    const mutations = new MutationObserver(schedule)
    for (let element: Element | null = list; element; element = composedParent(element)) {
      observer.observe(element)
      for (let sibling = element.previousElementSibling; sibling; sibling = sibling.previousElementSibling) observer.observe(sibling)
      // Do not observe row descendants: their own measurement observers handle
      // updates without rescanning ancestry or every data record on scroll.
      if (element !== list) mutations.observe(element, { attributes: true, attributeFilter: ['class', 'style', 'open'], childList: true })
      element.addEventListener('slotchange', schedule)
    }
    const win = list.ownerDocument.defaultView!
    win.addEventListener('resize', schedule, { passive: true })
    update()
    return () => {
      cancelAnimationFrame(frame)
      observer.disconnect()
      mutations.disconnect()
      for (let element: Element | null = list; element; element = composedParent(element)) element.removeEventListener('slotchange', schedule)
      win.removeEventListener('resize', schedule)
    }
  }, [windowed, style, listProps.className])

  // Key-based anchoring handles insertions, deletions and sorting without
  // changing the reading position. The key index is rebuilt only with data.
  useLayoutEffect(() => {
    if (!windowed) {
      anchor.current = null
      captureAnchor.current = null
      return
    }
    const previous = anchor.current
    if (previous && previous.keys !== keyed.keys && layout.owner) {
      const index = keyed.indices.get(previous.key)
      const row = index === undefined ? undefined : virtualizer.measurementsCache[index]
      if (row) virtualizer.scrollToOffset(row.start + previous.offset)
    }
    captureAnchor.current = () => {
      const offset = layout.owner ? scrollOffset(layout.owner) : virtualizer.scrollOffset ?? 0
      const first = virtualizer.getVirtualItemForOffset(offset)
      if (first && offset >= layout.margin) anchor.current = { keys: keyed.keys, key: first.key as ItemKey, offset: offset - first.start }
      else anchor.current = null
    }
    captureAnchor.current()
  })

  useLayoutEffect(() => {
    if (!focusRequest || !listRef.current) return
    const index = keyed.indices.get(focusRequest.key)
    if (index === undefined) {
      setFocusRequest(null)
      return
    }
    const row = listRef.current.querySelector<HTMLLIElement>(`:scope > li[data-index="${index}"]`)
    if (!row) return
    const stops = tabStops(row)
    const target = (focusRequest.backwards ? stops.at(-1) : stops[0]) ?? row
    target.focus({ preventScroll: true })
    virtualizer.scrollToIndex(index, { align: 'auto' })
    setFocusRequest(null)
  })

  const requestFocus = (index: number, backwards: boolean) => {
    if (index >= 0 && index < items.length) setFocusRequest({ key: keyed.keys[index], backwards })
  }
  const handleKeyDown = (event: KeyboardEvent<HTMLUListElement>) => {
    onKeyDown?.(event)
    if (!windowed || event.defaultPrevented || event.altKey || event.ctrlKey || event.metaKey || !(event.target instanceof Element) || nearestList(event.target) !== listRef.current) return
    const list = listRef.current!
    const row = ownRow(list, event.target)
    const index = row ? Number(row.dataset.index) : -1
    if (event.key === 'Tab') {
      // Composite widgets own their roving tab stop and must allow Tab to exit.
      if (listProps.role && ['tree', 'listbox', 'menu', 'menubar', 'radiogroup', 'tablist', 'grid'].includes(listProps.role)) return
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
      const next = event.key === 'ArrowDown' ? index + 1 : event.key === 'ArrowUp' ? index - 1 : event.key === 'Home' ? 0 : event.key === 'End' ? items.length - 1 : null
      if (next !== null) {
        event.preventDefault()
        requestFocus(next, false)
      }
    }
  }
  const rows = activeWindowing ? virtualizer.getVirtualItems() : windowed ? [] : items.map((item, index) => ({ index, key: itemKey(item, index), start: 0 }))

  return <ul
    {...listProps}
    ref={listRef}
    data-sc-virtual-list=""
    tabIndex={tabIndex ?? (activeWindowing && !listProps.role ? 0 : undefined)}
    style={activeWindowing ? { ...style, position: 'relative', boxSizing: 'border-box', height: virtualizer.getTotalSize() + layout.borders, flexShrink: 0, overflowAnchor: 'none' } : style}
    onKeyDown={handleKeyDown}
    onFocusCapture={(event) => {
      onFocusCapture?.(event)
      const row = ownRow(event.currentTarget, event.target)
      if (row) {
        const index = Number(row.dataset.index)
        setFocusedKey(itemKey(items[index], index))
      }
    }}
    onBlurCapture={(event) => {
      onBlurCapture?.(event)
      const list = event.currentTarget
      queueMicrotask(() => {
        if (listRef.current !== list) return
        const row = ownRow(list, activeElement(list.ownerDocument))
        const index = row ? Number(row.dataset.index) : -1
        setFocusedKey(index >= 0 && index < items.length ? itemKey(items[index], index) : null)
      })
    }}
  >{rows.map((row) => {
    const item = items[row.index]
    const suppliedProps = itemProps?.(item, row.index)
    const props = activeWindowing ? positionProps(suppliedProps, row.index, items.length) : suppliedProps ?? {}
    return <li
      {...props}
      key={row.key}
      ref={activeWindowing ? virtualizer.measureElement : undefined}
      data-index={row.index}
      tabIndex={props.tabIndex ?? (activeWindowing ? -1 : undefined)}
      style={activeWindowing ? { ...props.style, position: 'absolute', top: row.start - layout.margin, left: layout.left, right: layout.right, width: 'auto' } : props.style}
    >{renderItem(item, row.index)}</li>
  })}</ul>
}
