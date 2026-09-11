import type { ReactNode } from 'react'
import { useEffect, useRef } from 'react'

export interface DialogProps {
  open: boolean
  title: string
  onClose?: () => void
  onclose?: () => void
  children?: ReactNode
  actions?: ReactNode
  className?: string
  role?: 'dialog' | 'alertdialog'
  closedby?: string
  ariaLabel?: string
}

type DialogElement = HTMLElement & { open: boolean; show?: () => void; close?: () => void; updateComplete?: Promise<unknown> }

function focusFallback(): void {
  document.querySelector<HTMLElement>('[role="grid"][tabindex="0"], [role="tree"][tabindex="0"]')?.focus()
}

export function Dialog({
  open,
  title,
  onClose,
  onclose,
  children,
  actions,
  className,
  role = 'alertdialog',
  closedby = 'any',
  ariaLabel
}: DialogProps) {
  const ref = useRef<DialogElement | null>(null)
  const opener = useRef<HTMLElement | null>(null)
  const wasOpen = useRef(false)
  const closeHandler = onClose ?? onclose

  useEffect(() => {
    const element = ref.current
    if (!element) return
    const onNativeClose = () => {
      closeHandler?.()
      queueMicrotask(() => {
        const target = opener.current
        opener.current = null
        if (target?.isConnected && !target.hasAttribute('disabled') && !target.hasAttribute('aria-hidden')) {
          target.focus()
          if (document.activeElement === target) return
        }
        focusFallback()
      })
    }
    element.addEventListener('close', onNativeClose)
    element.addEventListener('cancel', onNativeClose)
    return () => {
      element.removeEventListener('close', onNativeClose)
      element.removeEventListener('cancel', onNativeClose)
    }
  }, [closeHandler])

  useEffect(() => {
    const element = ref.current
    if (!element) return
    if (open && !wasOpen.current) {
      opener.current = document.activeElement instanceof HTMLElement ? document.activeElement : null
      wasOpen.current = true
    }
    if (open) {
      element.open = true
    } else if (wasOpen.current) {
      wasOpen.current = false
      if (element.open) element.close?.()
      else {
        const target = opener.current
        opener.current = null
        queueMicrotask(() => {
          if (target?.isConnected && !target.hasAttribute('disabled') && !target.hasAttribute('aria-hidden')) target.focus()
          else focusFallback()
        })
      }
    }
  }, [open])

  return (
    <mdui-dialog
      ref={ref}
      className={className}
      headline={title}
      open={open}
      close-on-esc={closedby !== 'none'}
      close-on-overlay-click={closedby !== 'none'}
      aria-label={ariaLabel ?? title}
      role={role}
    >
      {children}
      {actions ? <span slot="action">{actions}</span> : null}
    </mdui-dialog>
  )
}
