import 'mdui/components/snackbar.js'
import { useEffect, useRef } from 'react'
import * as styles from './Snackbar.css'
import { cx } from './cx'

export interface SnackbarProps {
  message: string | null
  actionLabel?: string
  onAction?: () => void
  onDismiss?: () => void
  className?: string
}

export function Snackbar({ message, actionLabel, onAction, onDismiss, className }: SnackbarProps) {
  const ref = useRef<HTMLElement | null>(null)
  useEffect(() => {
    const element = ref.current
    if (!element) return
    const onClose = () => onDismiss?.()
    const onActionClick = () => onAction?.()
    element.addEventListener('close', onClose)
    element.addEventListener('action-click', onActionClick)
    return () => {
      element.removeEventListener('close', onClose)
      element.removeEventListener('action-click', onActionClick)
    }
  }, [onAction, onDismiss])
  if (!message) return null
  return (
    <mdui-snackbar ref={ref} className={cx(styles.root, className)} open action={actionLabel} closeable>
      {message}
    </mdui-snackbar>
  )
}
