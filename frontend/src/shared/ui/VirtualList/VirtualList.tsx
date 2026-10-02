import { useCallback, useLayoutEffect, useMemo, useRef, useState } from 'react'
import type { HTMLAttributes, KeyboardEvent, ReactNode } from 'react'
import { defaultRangeExtractor, useVirtualizer, useWindowVirtualizer, type Range } from '@tanstack/react-virtual'

type ItemKey = string | number

export interface VirtualListProps<T> extends Omit<HTMLAttributes<HTMLUListElement>, 'children'> {
  items: readonly T[]
  itemKey: (item: T, index: number) => ItemKey
  estimateSize: number
  renderItem: (item: T, index: number) => ReactNode
  itemProps?: (item: T, index: number) => HTMLAttributes<HTMLLIElement>
  /** Rows that stay mounted while scrolled out of view, such as one being edited. */
  pinnedKeys?: readonly ItemKey[]
}

const SMALL_LIST_LIMIT = 40
const OVERSCAN = 6
const EMPTY_KEYS: readonly ItemKey[] = []

/** Where the list sits in its scroll owner, and the box the absolutely placed rows must keep inside. */
interface Layout {
  /** The scrolling ancestor, or null when the document scrolls. */
  readonly owner: HTMLElement | null
  readonly visible: boolean
  /** The top of the list's padding box, measured from the top of the owner's scrolled content. */
  readonly margin: number
  readonly gap: number
  readonly top: number
  readonly bottom: number
  readonly left: number
  readonly right: number
  readonly borders: number
}

function documentScroller(element: Element): boolean {
  return element === (element.ownerDocument.scrollingElement ?? element.ownerDocument.documentElement)
}

// A growing overflow:auto wrapper is not a scroll owner.
function scrollOwner(list: HTMLUListElement): HTMLElement | null {
  for (let element: Element | null = list; element; element = element.parentElement) {
    if (
      element instanceof HTMLElement &&
      !documentScroller(element) &&
      /^(auto|scroll|overlay)$/.test(getComputedStyle(element).overflowY) &&
      element.scrollHeight > element.clientHeight + 1
    )
      return element
  }
  return null
}

function measureLayout(list: HTMLUListElement): Layout {
  const owner = scrollOwner(list)
  const css = getComputedStyle(list)
  const px = (value: string) => Number.parseFloat(value) || 0
  const top = list.getBoundingClientRect().top + list.clientTop
  const margin =
    owner === list
      ? 0
      : owner
        ? top - owner.getBoundingClientRect().top - owner.clientTop + owner.scrollTop
        : top + (list.ownerDocument.defaultView?.scrollY ?? 0)
  return {
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
}

const sameLayout = (a: Layout | null, b: Layout): boolean =>
  a !== null && (Object.keys(b) as (keyof Layout)[]).every((key) => a[key] === b[key])

function ownRow(list: HTMLUListElement, target: Element | null): HTMLLIElement | null {
  for (let element = target; element && element !== list; element = element.parentElement) {
    if (element.parentElement === list && element instanceof HTMLLIElement) return element
  }
  return null
}

function nearestList(target: Element): Element | null {
  for (let element: Element | null = target; element; element = element.parentElement) {
    if (element.hasAttribute('data-sc-virtual-list')) return element
  }
  return null
}

// Traverse only one mounted row, skipping hidden, inert and disabled subtrees.
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
    if (element.tabIndex >= 0) result.push(element)
    for (const child of element.children) visit(child)
  }
  visit(row)
  return result
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
  // list does not remount a focused control.
  const windowed = items.length > SMALL_LIST_LIMIT
  const listRef = useRef<HTMLUListElement>(null)
  const [layout, setLayout] = useState<Layout | null>(null)
  const [focusedKey, setFocusedKey] = useState<ItemKey | null>(null)
  const [focusRequest, setFocusRequest] = useState<{ key: ItemKey; backwards: boolean } | null>(null)
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
  const active = windowed && layout !== null && layout.visible
  // The first and last rows stay mounted so Tab and Shift+Tab enter the list at its ends.
  const retained = useMemo(() => {
    const indices = new Set<number>(items.length > 0 ? [0, items.length - 1] : [])
    for (const key of [...pinnedKeys, focusedKey, focusRequest?.key]) {
      const index = key == null ? undefined : keyed.indices.get(key)
      if (index !== undefined) indices.add(index)
    }
    return indices
  }, [items.length, keyed, pinnedKeys, focusedKey, focusRequest])

  const getItemKey = useCallback((index: number) => keyed.keys[index], [keyed])
  const rangeExtractor = useCallback(
    (range: Range) => [...new Set([...defaultRangeExtractor(range), ...retained])].sort((a, b) => a - b),
    [retained]
  )
  const options = {
    count: active ? items.length : 0,
    estimateSize: () => Math.max(1, estimateSize),
    getItemKey,
    rangeExtractor,
    overscan: OVERSCAN,
    scrollMargin: layout?.margin ?? 0,
    paddingStart: layout?.top ?? 0,
    paddingEnd: layout?.bottom ?? 0,
    gap: layout?.gap ?? 0
  }
  const owner = layout?.owner ?? null
  const onDocument = useWindowVirtualizer<HTMLLIElement>({ ...options, enabled: active && !owner })
  const onElement = useVirtualizer<HTMLElement, HTMLLIElement>({
    ...options,
    enabled: active && owner !== null,
    getScrollElement: () => owner,
    // The virtualizer scrolls its element to this offset when it attaches.
    initialOffset: () => owner?.scrollTop ?? 0
  })
  const virtualizer = owner ? onElement : onDocument

  useLayoutEffect(() => {
    const list = listRef.current
    if (!list || !windowed) return
    let frame = 0
    const update = () => {
      frame = 0
      const next = measureLayout(list)
      setLayout((previous) => (sameLayout(previous, next) ? previous : next))
    }
    const schedule = () => {
      frame ||= requestAnimationFrame(update)
    }
    // Content above the list moves its offset in the owner without resizing the list.
    const observer = new ResizeObserver(schedule)
    const mutations = new MutationObserver(schedule)
    for (let element: Element | null = list; element; element = element.parentElement) {
      observer.observe(element)
      for (let sibling = element.previousElementSibling; sibling; sibling = sibling.previousElementSibling)
        observer.observe(sibling)
      if (element !== list)
        mutations.observe(element, { attributes: true, attributeFilter: ['class', 'style', 'open'], childList: true })
    }
    const win = list.ownerDocument.defaultView!
    win.addEventListener('resize', schedule, { passive: true })
    update()
    return () => {
      cancelAnimationFrame(frame)
      observer.disconnect()
      mutations.disconnect()
      win.removeEventListener('resize', schedule)
    }
  }, [windowed, style, listProps.className])

  // The requested row is retained, so the render that sets the request also mounts it.
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
    // scrollToIndex sends the last row to the bottom of the owner, past any content below the list.
    row.scrollIntoView({ block: 'nearest' })
    setFocusRequest(null)
  }, [focusRequest, keyed, active])

  const requestFocus = (index: number, backwards: boolean) => {
    if (index >= 0 && index < items.length) setFocusRequest({ key: keyed.keys[index], backwards })
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
        const current = stops.findIndex((stop) => stop.contains(list.ownerDocument.activeElement))
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
  const scrollMargin = layout?.margin ?? 0
  const mounted = active
    ? virtualizer
        .getVirtualItems()
        .map((row) => ({ index: row.index, key: keyed.keys[row.index], top: row.start - scrollMargin }))
    : windowed
      ? []
      : items.map((item, index) => ({ index, key: itemKey(item, index), top: 0 }))

  return (
    <ul
      {...listProps}
      ref={listRef}
      data-sc-virtual-list=""
      tabIndex={tabIndex ?? (active && !listProps.role ? 0 : undefined)}
      style={
        active
          ? {
              ...style,
              position: 'relative',
              boxSizing: 'border-box',
              height: virtualizer.getTotalSize() + layout.borders,
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
          setFocusedKey(itemKey(items[index], index))
        }
      }}
      onBlurCapture={(event) => {
        onBlurCapture?.(event)
        const list = event.currentTarget
        queueMicrotask(() => {
          if (listRef.current !== list) return
          const row = ownRow(list, list.ownerDocument.activeElement)
          const index = row ? Number(row.dataset.index) : -1
          setFocusedKey(index >= 0 && index < items.length ? itemKey(items[index], index) : null)
        })
      }}
    >
      {mounted.map((row) => {
        const item = items[row.index]
        const suppliedProps = itemProps?.(item, row.index)
        const props = active ? positionProps(suppliedProps, row.index, items.length) : (suppliedProps ?? {})
        return (
          <li
            {...props}
            key={row.key}
            ref={active ? virtualizer.measureElement : undefined}
            data-index={row.index}
            tabIndex={props.tabIndex ?? (active ? -1 : undefined)}
            style={
              active
                ? {
                    ...props.style,
                    position: 'absolute',
                    top: row.top,
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
