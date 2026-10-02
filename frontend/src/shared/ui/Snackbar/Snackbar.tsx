import '@mantine/core/styles/CloseButton.layer.css'
import '@mantine/core/styles/Notification.layer.css'
import { Notification } from '@mantine/core'
import { useEffect, useEffectEvent } from 'react'
import { useI18n } from '../../../hooks/use-i18n'
import { cx } from '../cx'
import * as styles from './Snackbar.css'

const SHOWN_MS = 5000

export interface StowSnackbarProps {
  /** Null hides the snackbar. */
  message: string | null
  /** Runs when the close button is pressed or the message has been shown long enough. */
  onDismiss?: () => void
  className?: string
}

/** A short status message at the bottom of the viewport. */
export function StowSnackbar({ message, onDismiss, className }: StowSnackbarProps) {
  const { t } = useI18n()
  const dismiss = useEffectEvent(() => onDismiss?.())
  useEffect(() => {
    if (!message) return
    const timer = window.setTimeout(dismiss, SHOWN_MS)
    return () => window.clearTimeout(timer)
  }, [message])
  if (!message) return null
  return (
    <Notification
      role="status"
      onClose={onDismiss}
      closeButtonProps={{ 'aria-label': t('common.close') }}
      className={cx(styles.root, className)}
      classNames={{ description: styles.description, closeButton: styles.closeButton }}
    >
      {message}
    </Notification>
  )
}
