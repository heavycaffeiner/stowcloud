import type { MouseEvent as ReactMouseEvent, PointerEvent as ReactPointerEvent } from 'react'
import { useRef } from 'react'
import { selection } from '../../../lib/store/selection.store'
import { autoScrollStep, movedFar, rectBetween } from '../../../features/files/marquee'
import type { FileGridHandle } from '../../../features/files/FileGrid'
import type { FileViewHandle } from '../../../features/files/FileTable'
import type { BrowseState } from './types'

type Patch = (patch: Partial<BrowseState> | ((state: BrowseState) => Partial<BrowseState>)) => void

export function useBrowseMarquee({ mode, selectedNames, patch, tableRef, gridRef }: { mode: 'list' | 'grid'; selectedNames: ReadonlySet<string>; patch: Patch; tableRef: React.RefObject<FileViewHandle | null>; gridRef: React.RefObject<FileGridHandle | null> }) {
  const dragOrigin = useRef<{ x: number; y: number } | null>(null)
  const dragPointer = useRef({ x: 0, y: 0 })
  const dragBase = useRef<string[]>([])
  const dragFrame = useRef<number | null>(null)
  const marqueeActive = useRef(false)
  const controlSelector = 'button, input, a, [role="menuitem"], [role="menu"]'
  const contentSelector = `.sc-row, .sc-file-grid-card, ${controlSelector}`

  const updateMarquee = () => {
    const origin = dragOrigin.current
    if (!origin) return
    const pointer = dragPointer.current
    const rect = rectBetween(origin.x, origin.y, pointer.x + window.scrollX, pointer.y + window.scrollY)
    patch({ marqueeScroll: { x: window.scrollX, y: window.scrollY }, marqueeRect: rect })
    const activeView = mode === 'grid' ? gridRef.current : tableRef.current
    const hits = activeView?.entriesInRect(rect) ?? []
    selection.replace([...new Set([...dragBase.current, ...hits.map((entry) => entry.name)])])
  }

  const autoScrollTick = () => {
    if (!dragOrigin.current) return
    const activeView = mode === 'grid' ? gridRef.current : tableRef.current
    const bounds = activeView?.scrollBounds() ?? { top: 0, height: window.innerHeight }
    const step = autoScrollStep(dragPointer.current.y - bounds.top, bounds.height)
    if (step !== 0) {
      activeView?.scrollBy(step)
      updateMarquee()
    }
    dragFrame.current = requestAnimationFrame(autoScrollTick)
  }

  const swallowMarqueeClick = (event: MouseEvent) => {
    event.preventDefault()
    event.stopPropagation()
  }

  const onMarqueeKeyDown = (event: KeyboardEvent) => {
    if (event.key !== 'Escape') return
    event.preventDefault()
    selection.replace(dragBase.current)
    endMarquee()
  }

  const onMarqueePointerMove = (event: PointerEvent) => {
    const origin = dragOrigin.current
    if (!origin) return
    dragPointer.current = { x: event.clientX, y: event.clientY }
    if (!marqueeActive.current && !movedFar(origin.x - window.scrollX, origin.y - window.scrollY, event.clientX, event.clientY)) return
    if (!marqueeActive.current) {
      marqueeActive.current = true
      window.getSelection()?.removeAllRanges()
      dragFrame.current = requestAnimationFrame(autoScrollTick)
    }
    updateMarquee()
  }

  const endMarquee = () => {
    const wasDragging = marqueeActive.current
    marqueeActive.current = false
    dragOrigin.current = null
    patch({ marqueeRect: null })
    if (dragFrame.current !== null) cancelAnimationFrame(dragFrame.current)
    dragFrame.current = null
    window.removeEventListener('pointermove', onMarqueePointerMove)
    window.removeEventListener('pointerup', endMarquee)
    window.removeEventListener('keydown', onMarqueeKeyDown)
    if (wasDragging) {
      window.addEventListener('click', swallowMarqueeClick, { capture: true, once: true })
      setTimeout(() => window.removeEventListener('click', swallowMarqueeClick, true), 100)
    }
  }

  const onPointerDown = (event: ReactPointerEvent) => {
    if (event.pointerType !== 'mouse' || event.button !== 0) return
    if ((event.target as HTMLElement).closest(controlSelector)) return
    marqueeActive.current = false
    dragOrigin.current = { x: event.clientX + window.scrollX, y: event.clientY + window.scrollY }
    dragPointer.current = { x: event.clientX, y: event.clientY }
    dragBase.current = event.shiftKey || event.ctrlKey || event.metaKey ? [...selectedNames] : []
    window.addEventListener('pointermove', onMarqueePointerMove)
    window.addEventListener('pointerup', endMarquee)
    window.addEventListener('keydown', onMarqueeKeyDown)
  }

  const onEmptyAreaClick = (event: ReactMouseEvent) => {
    if ((event.target as HTMLElement).closest(contentSelector)) return
    selection.clear()
  }

  const openBlankMenu = (event: ReactMouseEvent) => {
    event.preventDefault()
    selection.clear()
    patch({ contextEntry: null, contextMenu: null, menuTrigger: event.currentTarget as HTMLElement, blankMenu: { x: event.clientX, y: event.clientY } })
  }

  return { onPointerDown, onEmptyAreaClick, openBlankMenu }
}
