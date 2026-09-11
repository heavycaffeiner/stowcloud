import { useEffect, useRef } from 'react'
import { useQuery } from '@tanstack/react-query'
import { sessionQuery } from '../query/session'
import { useI18n } from '../i18n/use-i18n'
import { FileTreeItem } from './FileTreeItem'
import './browse-ui.css'

export interface FileTreeProps {
  currentPath: string
  onNavigate: (path: string) => void
  overlay?: boolean
  onClose?: () => void
}

export function FileTree({ currentPath, onNavigate, overlay = false, onClose }: FileTreeProps) {
  const { t } = useI18n()
  const session = useQuery(sessionQuery())
  const dialog = useRef<HTMLDialogElement>(null)
  const wasOpen = useRef(false)
  const opener = useRef<HTMLElement | null>(null)

  useEffect(() => {
    const element = dialog.current
    if (!element) return
    if (overlay && !wasOpen.current) {
      opener.current = document.activeElement instanceof HTMLElement ? document.activeElement : null
      wasOpen.current = true
      if (!element.open) element.showModal()
      queueMicrotask(() => element.querySelector<HTMLElement>('[data-tree-label], [data-tree-more]')?.focus())
      return
    }
    if (!overlay && wasOpen.current) {
      wasOpen.current = false
      if (element.open) element.close()
      const target = opener.current
      opener.current = null
      queueMicrotask(() => {
        if (target?.isConnected && !target.hasAttribute('disabled') && !target.hasAttribute('aria-hidden')) target.focus()
        else document.querySelector<HTMLElement>('[role="grid"][tabindex="0"], [role="tree"][tabindex="0"]')?.focus()
      })
    }
  }, [overlay])

  const roots = session.data?.roots ?? []
  const tree = (
    <ul
      role="tree"
      tabIndex={0}
      aria-label={t('tree.folder_tree')}
      onFocus={(event) => {
        if (event.target !== event.currentTarget) return
        const target = event.currentTarget.querySelector<HTMLElement>('[aria-current="page"] [data-tree-label], [data-tree-label], [data-tree-more]')
        if (target) {
          event.currentTarget.tabIndex = -1
          target.focus()
        }
      }}
    >
      {roots.map((root, index) => <FileTreeItem key={root.label} path={`/${root.label}`} name={root.label} depth={0} currentPath={currentPath} onNavigate={onNavigate} position={index + 1} setSize={roots.length} initial={index === 0} />)}
    </ul>
  )

  if (!overlay) return <nav className="sc-file-tree" aria-label={t('tree.folder_tree')}>{tree}</nav>
  return (
    <dialog ref={dialog} className="sc-file-tree sc-file-tree--overlay" aria-label={t('tree.folder_tree')} onClick={(event) => { if (event.target === event.currentTarget) onClose?.() }} onCancel={(event) => { event.preventDefault(); onClose?.() }} onClose={() => { if (wasOpen.current) onClose?.() }}>
      <div className="sc-file-tree__overlay-header"><button type="button" onClick={onClose} aria-label={t('tree.close_folder_tree')}>×</button></div>
      <nav aria-label={t('tree.folder_tree')}>{tree}</nav>
    </dialog>
  )
}
