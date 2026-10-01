import { useEffect, useEffectEvent } from 'react'
import { useBlocker } from 'react-router-dom'
import { useBeforeUnload } from '../../../hooks/use-before-unload'
import { askLeaveEditor } from '../routes/EditDialogs'

export interface EditNavigationOptions {
  name: string
  dirty: boolean
  canSave: boolean
  save: () => Promise<boolean>
  discard: () => void
}

/** Holds a navigation away from unsaved changes until the user decides what happens to them. */
export function useEditNavigation({ name, dirty, canSave, save, discard }: EditNavigationOptions): void {
  const blocker = useBlocker(
    dirty ? ({ currentLocation, nextLocation }) => currentLocation.pathname !== nextLocation.pathname : false
  )
  const decide = useEffectEvent(async () => {
    const choice = await askLeaveEditor(name, canSave)
    if (choice === 'discard') discard()
    if (choice === 'discard' || (choice === 'save' && (await save()))) blocker.proceed?.()
    else blocker.reset?.()
  })
  useEffect(() => {
    if (blocker.state === 'blocked') void decide()
  }, [blocker.state])
  useBeforeUnload(dirty)
}
