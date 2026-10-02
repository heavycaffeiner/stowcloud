import type { MouseEvent as ReactMouseEvent, PointerEvent as ReactPointerEvent, RefObject } from 'react'
import { useRef, useState } from 'react'
import { selection, useSelectionStore } from '../selection'
import { autoScrollStep, movedFar, rectBetween } from '../logic/marquee'
import type { FileViewHandle } from '../components/FileTable'

/** The rubber band on screen, in viewport coordinates. */
export interface MarqueeBox {
  readonly left: number
  readonly top: number
  readonly width: number
  readonly height: number
}

const controlSelector = 'button, input, a, [role="menuitem"], [role="menu"]'
// File rows and grid cards are the only elements in the view that carry aria-selected.
const contentSelector = `[aria-selected], ${controlSelector}`

/** Rubber-band selection over the file view, and a click on empty space clearing the selection. */
export function useBrowseMarquee(view: RefObject<FileViewHandle | null>) {
  const [box, setBox] = useState<MarqueeBox | null>(null)
  const dragOrigin = useRef<{ x: number; y: number } | null>(null)
  const dragPointer = useRef({ x: 0, y: 0 })
  const dragBase = useRef<string[]>([])
  const dragFrame = useRef<number | null>(null)
  const marqueeActive = useRef(false)

  const updateMarquee = () => {
    const origin = dragOrigin.current
    if (!origin) return
    const pointer = dragPointer.current
    const rect = rectBetween(origin.x, origin.y, pointer.x + window.scrollX, pointer.y + window.scrollY)
    setBox({
      left: rect.left - window.scrollX,
      top: rect.top - window.scrollY,
      width: rect.right - rect.left,
      height: rect.bottom - rect.top
    })
    const hits = view.current?.entriesInRect(rect) ?? []
    selection.replace([...new Set([...dragBase.current, ...hits.map((entry) => entry.name)])])
  }

  const autoScrollTick = () => {
    if (!dragOrigin.current) return
    const bounds = view.current?.scrollBounds() ?? { top: 0, height: window.innerHeight }
    const step = autoScrollStep(dragPointer.current.y - bounds.top, bounds.height)
    if (step !== 0) {
      view.current?.scrollBy(step)
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
    if (
      !marqueeActive.current &&
      !movedFar(origin.x - window.scrollX, origin.y - window.scrollY, event.clientX, event.clientY)
    )
      return
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
    setBox(null)
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
    dragBase.current = event.shiftKey || event.ctrlKey || event.metaKey ? [...useSelectionStore.getState().names] : []
    window.addEventListener('pointermove', onMarqueePointerMove)
    window.addEventListener('pointerup', endMarquee)
    window.addEventListener('keydown', onMarqueeKeyDown)
  }

  const onClick = (event: ReactMouseEvent) => {
    if ((event.target as HTMLElement).closest(contentSelector)) return
    selection.clear()
  }

  return { box, onPointerDown, onClick }
}
