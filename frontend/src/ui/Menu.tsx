import 'mdui/components/menu.js'
import type { MouseEventHandler, ReactNode } from 'react'
import { useEffect, useRef } from 'react'
import { useI18n } from '../hooks/use-i18n'
import { useOutsideDismiss } from '../hooks/use-outside-dismiss'
import { useRestoreFocus } from '../hooks/use-restore-focus'
import { cx } from './cx'
import { Modal } from './Modal'
import * as styles from './Menu.css'

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
  const left = x === undefined ? undefined : align === 'start' ? Math.max(8, x) : undefined
  const right = x === undefined || align !== 'end' ? undefined : Math.max(8, window.innerWidth - x)
  const top = y === undefined ? undefined : Math.max(8, y)

  useRestoreFocus(open && !compact)

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
        <div className={styles.sheetScrim} onClick={close} aria-hidden="true" />
        <Modal open className={styles.sheet} label={t('common.main_menu')} onClose={close}>
          <div className={styles.sheetHandleWrap} aria-hidden="true">
            <div className={styles.sheetHandle} />
          </div>
          <div className={styles.sheetContent}>{children}</div>
        </Modal>
      </>
    )
  }
  return (
    <div
      ref={rootRef}
      className={styles.menuShell}
      style={{
        position: 'fixed',
        left,
        right,
        top,
        maxHeight: top === undefined ? undefined : `calc(100vh - ${top}px - 8px)`
      }}
    >
      <mdui-menu className={styles.menu}>{children}</mdui-menu>
    </div>
  )
}

export interface MenuListProps {
  id?: string
  label?: string
  className?: string
  children?: ReactNode
}

export function MenuList({ id, label, className, children }: MenuListProps) {
  return (
    <div id={id} className={cx(styles.list, className)} role="menu" aria-label={label}>
      {children}
    </div>
  )
}

export interface MenuItemProps {
  /** Makes the item one choice of a radio group, checked or not. */
  checked?: boolean
  title?: string
  className?: string
  onClick?: MouseEventHandler<HTMLButtonElement>
  children?: ReactNode
}

export function MenuItem({ checked, title, className, onClick, children }: MenuItemProps) {
  return (
    <button
      type="button"
      role={checked === undefined ? 'menuitem' : 'menuitemradio'}
      aria-checked={checked}
      title={title}
      className={cx(styles.item, className)}
      onClick={onClick}
    >
      {children}
    </button>
  )
}
