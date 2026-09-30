import type { ReactNode } from 'react'
import { useEffect, useRef } from 'react'
import { useI18n } from '../hooks/use-i18n'
import { useOutsideDismiss } from '../hooks/use-outside-dismiss'
export interface MenuProps {
  open: boolean
  onClose?: () => void
  compact?: boolean
  x?: number
  y?: number
  align?: 'start' | 'end'
  children?: ReactNode
}

export function Menu({ open, onClose, compact = false, x, y, align = 'start', children }: MenuProps) {
  const close = onClose ?? (() => undefined)
  const { t } = useI18n()
  const rootRef = useRef<HTMLDivElement>(null)
  const dialogRef = useRef<HTMLDialogElement | null>(null)
  const opener = useRef<HTMLElement | null>(null)
  const wasOpen = useRef(false)
  const left = x === undefined ? undefined : align === 'start' ? Math.max(8, x) : undefined
  const right = x === undefined || align !== 'end' ? undefined : Math.max(8, window.innerWidth - x)
  const top = y === undefined ? undefined : Math.max(8, y)

  useEffect(() => {
    if (compact) return
    if (open && !wasOpen.current) {
      opener.current = document.activeElement instanceof HTMLElement ? document.activeElement : null
      wasOpen.current = true
    } else if (!open && wasOpen.current) {
      wasOpen.current = false
      const target = opener.current
      opener.current = null
      queueMicrotask(() => {
        if (target?.isConnected && !target.hasAttribute('disabled') && !target.hasAttribute('aria-hidden'))
          target.focus()
      })
    }
  }, [compact, open])

  useEffect(() => {
    if (!compact || !open || !dialogRef.current) return
    const dialog = dialogRef.current
    opener.current = document.activeElement instanceof HTMLElement ? document.activeElement : null
    wasOpen.current = true
    try {
      if (!dialog.open) dialog.showModal()
    } catch {
      dialog.setAttribute('open', '')
    }
    return () => {
      if (dialog.open) dialog.close()
      else dialog.removeAttribute('open')
      if (!wasOpen.current) return
      wasOpen.current = false
      const target = opener.current
      opener.current = null
      queueMicrotask(() => {
        if (target?.isConnected && !target.hasAttribute('disabled') && !target.hasAttribute('aria-hidden'))
          target.focus()
      })
    }
  }, [compact, open])

  useOutsideDismiss(open && !compact, rootRef, close)

  // mdui-menu shows its items through a slot that exists only after its first render.
  useEffect(() => {
    if (!open || compact) return
    let cancelled = false
    void Promise.resolve(rootRef.current?.querySelector('mdui-menu')?.updateComplete).then(() => {
      if (!cancelled) rootRef.current?.querySelector<HTMLElement>('[role="menuitem"]')?.focus()
    })
    return () => {
      cancelled = true
    }
  }, [compact, open])

  if (!open) return null
  if (compact) {
    return (
      <>
        <div className="sc-sheet-scrim" onClick={close} aria-hidden="true" />
        <dialog
          ref={dialogRef}
          className="sc-sheet"
          aria-label={t('common.main_menu')}
          onClick={(event) => {
            // A press on the backdrop lands on the dialog itself.
            if (event.target === event.currentTarget) close()
          }}
          onCancel={(event) => {
            event.preventDefault()
            close()
          }}
        >
          <div className="sc-sheet-handle-wrap" aria-hidden="true">
            <div className="sc-sheet-handle" />
          </div>
          <div className="sc-sheet-content">{children}</div>
        </dialog>
      </>
    )
  }
  return (
    <div
      ref={rootRef}
      className="sc-menu-shell"
      style={{
        position: 'fixed',
        left,
        right,
        top,
        maxHeight: top === undefined ? undefined : `calc(100vh - ${top}px - 8px)`
      }}
    >
      <mdui-menu>{children}</mdui-menu>
    </div>
  )
}
