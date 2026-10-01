import 'mdui/components/dialog.js'
import type { KeyboardEvent, ReactNode } from 'react'
import { useRef } from 'react'
import { useEventListener } from '../hooks/use-event-listener'
import { useRestoreFocus } from '../hooks/use-restore-focus'
import { cx } from './cx'
import * as styles from './Dialog.css'

export interface DialogProps {
  open: boolean
  title: string
  /** Keeps the title as the accessible name only, for dialogs that draw their own header. */
  hideTitle?: boolean
  /** Asks the owner to close: Escape, or a press on the scrim. Closing is up to the owner. */
  onClose?: () => void
  /** Runs once the close animation has finished, so an overlay can unmount without cutting it short. */
  onClosed?: () => void
  /** False keeps Escape and the scrim from asking to close. */
  dismissible?: boolean
  children?: ReactNode
  actions?: ReactNode
  className?: string
  /** wide: grows with its content up to the viewport. viewer: fills the viewport for media. */
  size?: 'wide' | 'viewer'
  role?: 'dialog' | 'alertdialog'
  ariaLabel?: string
  /** Runs before the dialog's own Escape handling; preventDefault skips it. */
  onKeyDown?: (event: KeyboardEvent<HTMLElement>) => void
}

/** A modal dialog whose open state belongs to the caller; mdui never closes it on its own. */
export function Dialog({
  open,
  title,
  hideTitle = false,
  onClose,
  onClosed,
  dismissible = true,
  children,
  actions,
  className,
  size,
  role = 'alertdialog',
  ariaLabel,
  onKeyDown
}: DialogProps) {
  const ref = useRef<HTMLElement>(null)
  useRestoreFocus(open)
  useEventListener(ref, 'overlay-click', () => {
    if (dismissible) onClose?.()
  })
  // mdui shows the dialog and moves focus into it a frame later. Moving it right after the dialog shows
  // keeps a key pressed in between, Escape included, from landing on the page underneath.
  useEventListener(ref, 'open', (event) => {
    const dialog = ref.current
    if (event.target !== dialog || !dialog) return
    queueMicrotask(() =>
      (
        dialog.querySelector<HTMLElement>('[autofocus]') ??
        dialog.shadowRoot?.querySelector<HTMLElement>('[part~="panel"]')
      )?.focus({ preventScroll: true })
    )
  })
  // mdui popups nested inside the dialog fire their own bubbling `closed`.
  useEventListener(ref, 'closed', (event) => {
    if (event.target === ref.current) onClosed?.()
  })

  return (
    <mdui-dialog
      ref={ref}
      className={cx(styles.root, size && styles[size], className)}
      headline={hideTitle ? undefined : title}
      open={open}
      aria-label={ariaLabel ?? title}
      role={role}
      onKeyDown={(event) => {
        onKeyDown?.(event)
        if (event.key !== 'Escape' || event.defaultPrevented || !dismissible) return
        event.preventDefault()
        onClose?.()
      }}
    >
      {children}
      {actions ? (
        <span slot="action" className={styles.actions}>
          {actions}
        </span>
      ) : null}
    </mdui-dialog>
  )
}
