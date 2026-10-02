import '@mantine/core/styles/Overlay.layer.css'
import '@mantine/core/styles/Paper.layer.css'
import '@mantine/core/styles/ModalBase.layer.css'
import '@mantine/core/styles/Modal.layer.css'
import { Modal } from '@mantine/core'
import { type KeyboardEvent, type ReactNode, useCallback } from 'react'
import { useRestoreFocus } from '../../../hooks/use-restore-focus'
import { cx } from '../cx'
import * as styles from './Dialog.css'

export interface StowDialogProps {
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
  /** Runs before the dialog's own Escape handling; preventDefault skips it. */
  onKeyDown?: (event: KeyboardEvent<HTMLElement>) => void
}

const passthrough = ({ children }: { children?: ReactNode }) => children

/** A modal dialog whose open state belongs to the caller. Focus goes to the first `data-autofocus` element. */
export function StowDialog({
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
  onKeyDown
}: StowDialogProps) {
  // Mantine's own focus return waits a tick, which loses to a dialog that opens from this one closing.
  useRestoreFocus(open)
  // Mantine fixes the role to dialog, so the alert role goes on after it renders.
  const roleRef = useCallback((node: HTMLElement | null) => node?.setAttribute('role', role), [role])
  const close = () => onClose?.()

  return (
    <Modal.Root
      opened={open}
      onClose={close}
      onExitTransitionEnd={onClosed}
      closeOnEscape={false}
      closeOnClickOutside={dismissible}
      returnFocus={false}
      centered
      scrollAreaComponent={passthrough}
      // Children handle the key first, so a field that uses Escape can keep the dialog open.
      onKeyDown={(event: KeyboardEvent<HTMLElement>) => {
        onKeyDown?.(event)
        if (event.key !== 'Escape' || event.defaultPrevented || !dismissible) return
        event.preventDefault()
        close()
      }}
    >
      <Modal.Overlay className={styles.overlay} />
      <Modal.Content
        ref={roleRef}
        aria-label={hideTitle ? title : undefined}
        // Mantine puts a className on the inner wrapper as well, so each part takes its own.
        classNames={{
          content: cx(styles.content, size && styles[size], className),
          inner: cx(styles.inner, size === 'viewer' && styles.viewerInner)
        }}
      >
        {hideTitle ? null : (
          <Modal.Header className={styles.header}>
            <Modal.Title className={styles.title}>{title}</Modal.Title>
          </Modal.Header>
        )}
        <Modal.Body className={styles.body}>{children}</Modal.Body>
        {actions ? <div className={styles.actions}>{actions}</div> : null}
      </Modal.Content>
    </Modal.Root>
  )
}
