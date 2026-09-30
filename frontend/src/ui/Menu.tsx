import type { ReactNode } from 'react'
import { useEffect, useRef } from 'react'
import { useI18n } from '../hooks/use-i18n'
import { useOutsideDismiss } from '../hooks/use-outside-dismiss'
import { useRestoreFocus } from '../hooks/use-restore-focus'

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
  const left = x === undefined ? undefined : align === 'start' ? Math.max(8, x) : undefined
  const right = x === undefined || align !== 'end' ? undefined : Math.max(8, window.innerWidth - x)
  const top = y === undefined ? undefined : Math.max(8, y)

  useRestoreFocus(open)

  useEffect(() => {
    if (!compact || !open || !dialogRef.current) return
    const dialog = dialogRef.current
    try {
      if (!dialog.open) dialog.showModal()
    } catch {
      dialog.setAttribute('open', '')
    }
    return () => {
      if (dialog.open) dialog.close()
      else dialog.removeAttribute('open')
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
