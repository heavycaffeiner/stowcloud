import { useRef } from 'react'
import type { HTMLAttributes, MouseEvent } from 'react'
import type { Entry } from '../../lib/api/types'

const TAP_MAX_MS = 450
const TAP_MOVE_PX = 12

export type ActivationHandlers = Pick<HTMLAttributes<HTMLDivElement>, 'onClick' | 'onPointerDown' | 'onPointerMove' | 'onPointerUp' | 'onPointerCancel'>
type Gesture = { path: string; pointerType: string; pointerId: number; time: number; x: number; y: number; valid: boolean; released: boolean }

// Touch opens on one completed tap; pointer devices keep click-to-select and double-click-to-open.
export function useFileActivation(onSelect: (entry: Entry, index: number, event: MouseEvent<HTMLDivElement>) => void, onOpen: (entry: Entry) => void) {
  const gestureRef = useRef<Gesture | null>(null)
  const cancel = () => {
    gestureRef.current = null
  }
  const invalidate = () => {
    if (gestureRef.current) gestureRef.current.valid = false
  }
  const handlers = (entry: Entry, index: number): ActivationHandlers => ({
    onPointerDown(event) {
      if (!event.isPrimary || event.button !== 0) {
        invalidate()
        return
      }
      gestureRef.current = { path: entry.path, pointerType: event.pointerType, pointerId: event.pointerId, time: Date.now(), x: event.clientX, y: event.clientY, valid: true, released: false }
    },
    onPointerMove(event) {
      const start = gestureRef.current
      if (start?.pointerId === event.pointerId && (Math.abs(event.clientX - start.x) >= TAP_MOVE_PX || Math.abs(event.clientY - start.y) >= TAP_MOVE_PX)) invalidate()
    },
    onPointerUp(event) {
      const start = gestureRef.current
      if (!start || start.pointerId !== event.pointerId) return
      start.released = true
      if (start.path !== entry.path || Math.abs(event.clientX - start.x) >= TAP_MOVE_PX || Math.abs(event.clientY - start.y) >= TAP_MOVE_PX || (start.pointerType === 'touch' && Date.now() - start.time >= TAP_MAX_MS)) invalidate()
    },
    onPointerCancel: invalidate,
    onClick(event) {
      const gesture = gestureRef.current
      gestureRef.current = null
      const native = event.nativeEvent as globalThis.PointerEvent
      const pointerType = native.pointerType || gesture?.pointerType || 'mouse'
      if (gesture && (gesture.path !== entry.path || !gesture.valid || !gesture.released)) return
      if (pointerType === 'touch') {
        if (!event.shiftKey && !event.ctrlKey && !event.metaKey && !event.altKey) onOpen(entry)
        return
      }
      onSelect(entry, index, event)
      if (event.shiftKey || event.ctrlKey || event.metaKey || event.altKey) return
      if ((pointerType === 'mouse' || pointerType === 'pen') && event.detail === 2) onOpen(entry)
    }
  })
  return { handlers, cancel }
}
