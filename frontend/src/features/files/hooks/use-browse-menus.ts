import type { Dispatch, MouseEvent as ReactMouseEvent } from 'react'
import { useSignalEffect } from '@preact/signals-react'
import { createMenuRequest } from '../create-menu'
import type { BrowseState } from '../logic/browse-types'
import type { StatePatch } from '../../../lib/merge-state'

type Patch = Dispatch<StatePatch<BrowseState>>
type Translate = (key: string, params?: Record<string, string | number>) => string

export function useBrowseMenus({
  state,
  patch,
  canCreate,
  t
}: {
  state: BrowseState
  patch: Patch
  canCreate: boolean
  t: Translate
}) {
  const { newMenuOpen, overflowOpen, sortMenuOpen, newMenuTrigger, overflowTrigger, sortMenuTrigger } = state
  const closeSort = () => {
    patch({ sortMenuOpen: false })
    sortMenuTrigger?.focus()
    patch({ sortMenuTrigger: null })
  }
  const openSort = (event: ReactMouseEvent<HTMLElement>) => {
    if (sortMenuOpen) {
      closeSort()
      return
    }
    event.stopPropagation()
    const trigger = event.currentTarget
    const rect = trigger.getBoundingClientRect()
    patch({ sortMenuTrigger: trigger, sortMenuPosition: { x: rect.right, y: rect.bottom + 4 }, sortMenuOpen: true })
  }
  const closeNewMenu = () => {
    patch({ newMenuOpen: false })
    newMenuTrigger?.focus()
    patch({ newMenuTrigger: null })
  }
  const openNewMenu = (event: ReactMouseEvent<HTMLElement>) => {
    if (newMenuOpen) {
      closeNewMenu()
      return
    }
    event.stopPropagation()
    const trigger = event.currentTarget
    const rect = trigger.getBoundingClientRect()
    patch({
      newMenuTrigger: trigger,
      newMenuPosition: { x: rect.right, y: rect.bottom + 4, align: 'end' },
      newMenuOpen: true
    })
  }
  const closeOverflow = () => {
    patch({ overflowOpen: false })
    overflowTrigger?.focus()
    patch({ overflowTrigger: null })
  }
  const openOverflow = (event: ReactMouseEvent<HTMLElement>) => {
    if (overflowOpen) {
      closeOverflow()
      return
    }
    event.stopPropagation()
    const trigger = event.currentTarget
    const rect = trigger.getBoundingClientRect()
    patch({ overflowTrigger: trigger, overflowPosition: { x: rect.right, y: rect.bottom + 4 }, overflowOpen: true })
  }
  const openFilterMenu = (kind: 'type' | 'date', event: ReactMouseEvent<HTMLElement>) => {
    if (kind === 'type' ? state.typeMenuOpen : state.dateMenuOpen) {
      patch(kind === 'type' ? { typeMenuOpen: false } : { dateMenuOpen: false })
      return
    }
    const rect = event.currentTarget.getBoundingClientRect()
    patch(
      kind === 'type'
        ? { typeMenuPosition: { x: rect.left, y: rect.bottom + 4 }, typeMenuOpen: true }
        : { dateMenuPosition: { x: rect.left, y: rect.bottom + 4 }, dateMenuOpen: true }
    )
  }
  useSignalEffect(() => {
    const anchor = createMenuRequest.value
    if (anchor === null) return
    createMenuRequest.value = null
    if (canCreate) patch({ newMenuTrigger: null, newMenuPosition: anchor, newMenuOpen: true })
    else patch({ snackbar: t('error.acl_denied') })
  })
  return { openSort, closeSort, openNewMenu, closeNewMenu, openOverflow, closeOverflow, openFilterMenu }
}
