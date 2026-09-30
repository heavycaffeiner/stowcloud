import 'mdui/components/snackbar.js'
import { useEffect, useRef } from 'react'
import * as styles from './Snackbar.css'

export interface SnackbarProps {
  message: string | null
  actionLabel?: string
  onAction?: () => void
  onDismiss?: () => void
}

export function Snackbar({ message, actionLabel, onAction, onDismiss }: SnackbarProps) {
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
    <mdui-snackbar ref={ref} className={styles.root} open action={actionLabel} closeable>
      {message}
    </mdui-snackbar>
  )
}
