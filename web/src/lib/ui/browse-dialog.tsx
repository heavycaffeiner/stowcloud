import type { ReactNode } from 'react'
import { useEffect, useRef } from 'react'
import { useI18n } from '../i18n/use-i18n'
import './browse-ui.css'

export interface BrowseDialogProps {
  open: boolean
  title: string
  onClose: () => void
  children: ReactNode
  actions?: ReactNode
}

export function BrowseDialog({ open, title, onClose, children, actions }: BrowseDialogProps) {
  const { t } = useI18n()
  const ref = useRef<HTMLDialogElement>(null)
  const opener = useRef<HTMLElement | null>(null)
  const wasOpen = useRef(false)
  const closingByParent = useRef(false)

  useEffect(() => {
    const element = ref.current
    if (!element) return
    const onNativeClose = () => {
      if (!closingByParent.current) onClose()
      closingByParent.current = false
      queueMicrotask(() => {
        const target = opener.current
        opener.current = null
        if (target?.isConnected && !target.hasAttribute('disabled') && !target.hasAttribute('aria-hidden')) {
          target.focus()
          if (document.activeElement === target) return
        }
        document.querySelector<HTMLElement>('[role="grid"][tabindex="0"], [role="tree"][tabindex="0"]')?.focus()
      })
    }
    element.addEventListener('close', onNativeClose)
    return () => element.removeEventListener('close', onNativeClose)
  }, [onClose])

  useEffect(() => {
    const element = ref.current
    if (!element) return
    if (open && !wasOpen.current) {
      opener.current = document.activeElement instanceof HTMLElement ? document.activeElement : null
      wasOpen.current = true
      if (!element.open) element.showModal()
      queueMicrotask(() => element.querySelector<HTMLElement>('input, textarea, select, button:not([aria-label="Close"])')?.focus())
      return
    }
    if (!open && wasOpen.current) {
      wasOpen.current = false
      if (element.open) {
        closingByParent.current = true
        element.close()
      }
    }
  }, [open])

  return (
    <dialog
      ref={ref}
      className="sc-browse-dialog"
      aria-labelledby="sc-browse-dialog-title"
      onCancel={(event) => { event.preventDefault(); onClose() }}
      onClick={(event) => { if (event.target === event.currentTarget) onClose() }}
    >
      <div className="sc-browse-dialog__surface">
        <header>
          <h2 id="sc-browse-dialog-title">{title}</h2>
          <button type="button" aria-label={t('common.close')} onClick={onClose}>×</button>
        </header>
        <div className="sc-browse-dialog__body">{children}</div>
        {actions ? <footer>{actions}</footer> : null}
      </div>
    </dialog>
  )
}
