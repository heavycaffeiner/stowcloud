import type { ReactNode } from 'react'
import { useEffect, useEffectEvent, useRef } from 'react'
import { useRestoreFocus } from '../hooks/use-restore-focus'

export interface ModalProps {
  open: boolean
  label: string
  /** Asks the owner to close: Escape, the back gesture, or a press on the backdrop. */
  onClose: () => void
  id?: string
  className?: string
  /** Picks the element to focus once the dialog shows; the browser's own choice stands otherwise. */
  initialFocus?: (dialog: HTMLDialogElement) => HTMLElement | null | undefined
  children?: ReactNode
}

/** A native modal dialog whose open state belongs to the caller, for sheets and drawers. */
export function Modal({ open, label, onClose, id, className, initialFocus, children }: ModalProps) {
  const ref = useRef<HTMLDialogElement>(null)
  const focusFirst = useEffectEvent((dialog: HTMLDialogElement) => initialFocus?.(dialog)?.focus())
  useRestoreFocus(open)

  useEffect(() => {
    const dialog = ref.current
    if (!open || !dialog) return
    try {
      if (!dialog.open) dialog.showModal()
    } catch {
      // Environments without showModal still get the content, only without the top layer.
      dialog.setAttribute('open', '')
    }
    queueMicrotask(() => focusFirst(dialog))
    return () => {
      if (dialog.open) dialog.close()
      else dialog.removeAttribute('open')
    }
  }, [open])

  if (!open) return null
  return (
    <dialog
      ref={ref}
      id={id}
      className={className}
      aria-label={label}
      onClick={(event) => {
        // A press on the backdrop lands on the dialog itself.
        if (event.target === event.currentTarget) onClose()
      }}
      onCancel={(event) => {
        event.preventDefault()
        onClose()
      }}
      // The browser can close a modal without a cancelable cancel event, such as on a back gesture.
      // A close queued by an effect cleanup can land after the dialog is shown again, so it is ignored.
      onClose={(event) => {
        if (!event.currentTarget.open) onClose()
      }}
    >
      {children}
    </dialog>
  )
}
