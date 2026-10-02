import '@mantine/core/styles/Overlay.layer.css'
import '@mantine/core/styles/Paper.layer.css'
import '@mantine/core/styles/ModalBase.layer.css'
import '@mantine/core/styles/Drawer.layer.css'
import { Drawer } from '@mantine/core'
import type { ReactNode } from 'react'
import { useRestoreFocus } from '../../../hooks/use-restore-focus'
import { cx } from '../cx'
import { overlay } from '../Dialog/Dialog.css'
import * as styles from './Drawer.css'

export interface StowDrawerProps {
  open: boolean
  label: string
  /** Asks the owner to close: Escape, or a press on the scrim. */
  onClose: () => void
  /** Runs once the close animation has finished, so an overlay can unmount without cutting it short. */
  onClosed?: () => void
  position?: 'left' | 'right' | 'top' | 'bottom'
  id?: string
  className?: string
  /** Focus goes to the first `data-autofocus` element inside, else the first tabbable one. */
  children?: ReactNode
}

const passthrough = ({ children }: { children?: ReactNode }) => children

/** A modal panel attached to one edge of the viewport, for navigation and sheets. */
export function StowDrawer({
  open,
  label,
  onClose,
  onClosed,
  position = 'left',
  id,
  className,
  children
}: StowDrawerProps) {
  // Mantine's own focus return waits a tick, which loses to a dialog that opens from this one closing.
  useRestoreFocus(open)
  return (
    <Drawer.Root
      opened={open}
      onClose={onClose}
      onExitTransitionEnd={onClosed}
      position={position}
      returnFocus={false}
      scrollAreaComponent={passthrough}
    >
      <Drawer.Overlay className={overlay} />
      <Drawer.Content
        id={id}
        aria-label={label}
        // Mantine puts a className on the inner wrapper as well, so the panel takes its classes by part.
        classNames={{ content: cx(styles.content, styles.position[position], className) }}
      >
        {children}
      </Drawer.Content>
    </Drawer.Root>
  )
}
