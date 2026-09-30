import type { RefObject } from 'react'
import { useEventListener } from './use-event-listener'

/**
 * While `open`, calls `onDismiss` on Escape or on a press outside `ref`. A press
 * on an open popup trigger is left to that trigger's own click, so it toggles
 * instead of closing and reopening.
 */
export function useOutsideDismiss(open: boolean, ref: RefObject<Element | null>, onDismiss: () => void): void {
  const target = open ? window : null
  useEventListener(target, 'keydown', (event) => {
    if (event.key !== 'Escape') return
    event.preventDefault()
    onDismiss()
  })
  useEventListener(
    target,
    'pointerdown',
    (event) => {
      const pressed = event.target
      if (!(pressed instanceof Node) || ref.current?.contains(pressed)) return
      if (pressed instanceof Element && pressed.closest('[aria-haspopup][aria-expanded="true"]')) return
      onDismiss()
    },
    { capture: true }
  )
}
