import type { MouseEvent as ReactMouseEvent } from 'react'
import { useEffect } from 'react'
import type { NewActionAnchor } from '../../AppShell'
import type { BrowseState } from './types'

type Patch = (patch: Partial<BrowseState> | ((state: BrowseState) => Partial<BrowseState>)) => void
type Translate = (key: string, params?: Record<string, string | number>) => string

export function useBrowseMenus({ state, patch, canCreate, t }: { state: BrowseState; patch: Patch; canCreate: boolean; t: Translate }) {
  const { newMenuOpen, overflowOpen, newMenuTrigger, overflowTrigger, sortMenuTrigger } = state
  const closeSort = () => { patch({ sortMenuOpen: false }); sortMenuTrigger?.focus(); patch({ sortMenuTrigger: null }) }
  const openSort = (event: ReactMouseEvent<HTMLElement>) => {
    event.stopPropagation()
    const trigger = event.currentTarget
    const rect = trigger.getBoundingClientRect()
    patch({ sortMenuTrigger: trigger, sortMenuPosition: { x: rect.right, y: rect.bottom + 4 }, sortMenuOpen: true })
  }
  const closeNewMenu = () => { patch({ newMenuOpen: false }); newMenuTrigger?.focus(); patch({ newMenuTrigger: null }) }
  const openNewMenu = (event: ReactMouseEvent<HTMLElement>) => {
    if (newMenuOpen) { closeNewMenu(); return }
    event.stopPropagation()
    const trigger = event.currentTarget
    const rect = trigger.getBoundingClientRect()
    patch({ newMenuTrigger: trigger, newMenuPosition: { x: rect.right, y: rect.bottom + 4, align: 'end' }, newMenuOpen: true })
  }
  const closeOverflow = () => { patch({ overflowOpen: false }); overflowTrigger?.focus(); patch({ overflowTrigger: null }) }
  const openOverflow = (event: ReactMouseEvent<HTMLElement>) => {
    if (overflowOpen) { closeOverflow(); return }
    event.stopPropagation()
    const trigger = event.currentTarget
    const rect = trigger.getBoundingClientRect()
    patch({ overflowTrigger: trigger, overflowPosition: { x: rect.right, y: rect.bottom + 4 }, overflowOpen: true })
  }
  const openFilterMenu = (kind: 'type' | 'date', event: ReactMouseEvent<HTMLElement>) => {
    const rect = event.currentTarget.getBoundingClientRect()
    patch(kind === 'type' ? { typeMenuPosition: { x: rect.left, y: rect.bottom + 4 }, typeMenuOpen: true } : { dateMenuPosition: { x: rect.left, y: rect.bottom + 4 }, dateMenuOpen: true })
  }
  useEffect(() => {
    const handleNew = (event: Event) => {
      if (!canCreate) { patch({ snackbar: t('error.acl_denied') }); return }
      const anchor = (event as CustomEvent<NewActionAnchor | undefined>).detail
      patch({ newMenuTrigger: null, newMenuPosition: anchor ?? { x: 80, y: 120, align: 'start' }, newMenuOpen: true })
    }
    window.addEventListener('stowcloud:new', handleNew)
    return () => window.removeEventListener('stowcloud:new', handleNew)
  }, [canCreate, patch, t])
  useEffect(() => {
    if (!newMenuOpen && !overflowOpen) return
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return
      event.preventDefault()
      if (newMenuOpen) closeNewMenu(); else closeOverflow()
    }
    const onPointerDown = (event: PointerEvent) => {
      const target = event.target as Element | null
      if (target?.closest('.sc-browse-new-menu, [aria-expanded="true"]')) return
      if (newMenuOpen) closeNewMenu()
      if (overflowOpen) closeOverflow()
    }
    window.addEventListener('keydown', onKeyDown)
    window.addEventListener('pointerdown', onPointerDown, true)
    return () => { window.removeEventListener('keydown', onKeyDown); window.removeEventListener('pointerdown', onPointerDown, true) }
  }, [newMenuOpen, overflowOpen, newMenuTrigger, overflowTrigger])
  useEffect(() => {
    if (!newMenuOpen && !overflowOpen && state.contextMenu === null && state.blankMenu === null) return
    queueMicrotask(() => document.querySelector<HTMLElement>('.sc-browse-new-menu button')?.focus())
  }, [newMenuOpen, overflowOpen, state.contextMenu, state.blankMenu])
  return { openSort, closeSort, openNewMenu, closeNewMenu, openOverflow, closeOverflow, openFilterMenu }
}
