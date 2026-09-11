import { useEffect, useRef } from 'react'
import type { ReactNode } from 'react'
import { useI18n } from '../i18n/use-i18n'
import { SearchPanel } from './SearchPanel'
import { Icon } from './Icon'
import { IconButton } from './IconButton'

export interface SearchSheetProps {
  readonly open: boolean
  readonly scope?: string
  readonly onclose: () => void
}

export function SearchSheet({ open, scope = '', onclose }: SearchSheetProps) {
  const { t } = useI18n()
  const dialogRef = useRef<HTMLDialogElement | null>(null)
  const panelRef = useRef<{ focus: () => void } | null>(null)
  const triggerRef = useRef<HTMLElement | null>(null)

  useEffect(() => {
    const dialog = dialogRef.current
    if (!open || !dialog) return
    triggerRef.current = document.activeElement instanceof HTMLElement ? document.activeElement : null
    try {
      if (!dialog.open) dialog.showModal()
    } catch {
      dialog.setAttribute('open', '')
    }
    queueMicrotask(() => panelRef.current?.focus())
    return () => {
      if (dialog.open) dialog.close()
      else dialog.removeAttribute('open')
      triggerRef.current?.focus()
      triggerRef.current = null
    }
  }, [open])

  if (!open) return null

  const trailing: ReactNode = (
    <IconButton label={t('search.close')} onClick={onclose}><Icon name="close" /></IconButton>
  )

  return (
    <dialog
      ref={dialogRef}
      className="sc-search-sheet"
      aria-label={t('search.title')}
      onClick={(event) => {
        if (event.target === event.currentTarget) onclose()
      }}
      onCancel={(event) => {
        event.preventDefault()
        onclose()
      }}
      onClose={() => onclose()}
    >
      <div className="sc-search-sheet__body">
        <SearchPanel ref={panelRef} scope={scope} autofocus onnavigated={onclose} trailing={trailing} />
      </div>
    </dialog>
  )
}
