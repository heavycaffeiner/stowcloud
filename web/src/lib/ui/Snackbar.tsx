import { useEffect, useRef, useSyncExternalStore } from 'react'

export interface SnackbarProps {
  message?: string | null
  actionLabel?: string
  onAction?: () => void
  onaction?: () => void
  onDismiss?: () => void
  ondismiss?: () => void
}

interface GlobalSnackbarState {
  message: string | null
  actionLabel?: string
  onAction?: () => void
}

let globalState: GlobalSnackbarState = { message: null }
const listeners = new Set<() => void>()
const subscribe = (listener: () => void): (() => void) => {
  listeners.add(listener)
  return () => listeners.delete(listener)
}
const getGlobalState = (): GlobalSnackbarState => globalState
const publish = (next: GlobalSnackbarState): void => {
  globalState = next
  listeners.forEach((listener) => listener())
}
const dismissGlobal = (): void => publish({ message: null })
export function Snackbar({ message: explicitMessage, actionLabel: explicitActionLabel, onAction: explicitOnAction, onaction, onDismiss, ondismiss }: SnackbarProps) {
  const global = useSyncExternalStore(subscribe, getGlobalState, getGlobalState)
  const message = explicitMessage === undefined ? global.message : explicitMessage
  const actionLabel = explicitActionLabel ?? (explicitMessage === undefined ? global.actionLabel : undefined)
  const action = explicitOnAction ?? onaction ?? (explicitMessage === undefined ? global.onAction : undefined)
  const dismiss = onDismiss ?? ondismiss ?? (explicitMessage === undefined ? dismissGlobal : undefined)
  const ref = useRef<HTMLElement | null>(null)
  useEffect(() => {
    const element = ref.current
    if (!element) return
    const onClose = () => dismiss?.()
    const onActionClick = () => action?.()
    element.addEventListener('close', onClose)
    element.addEventListener('action-click', onActionClick)
    return () => {
      element.removeEventListener('close', onClose)
      element.removeEventListener('action-click', onActionClick)
    }
  }, [action, dismiss])
  if (!message) return null
  return <mdui-snackbar ref={ref} open action={actionLabel} closeable>{message}</mdui-snackbar>
}

export function showSnackbar(message: string, actionLabel?: string, onAction?: () => void): void {
  publish({ message, actionLabel, onAction })
}
