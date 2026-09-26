import { useEffect } from 'react'
import type { Blocker } from 'react-router-dom'
import type { EditActions } from './use-edit-state'

export interface EditNavigationOptions {
  dirty: boolean
  blocker: Blocker
  actions: Pick<EditActions, 'setLeaveDialog'>
}

export function useEditNavigation({ dirty, blocker, actions }: EditNavigationOptions): void {
  useEffect(() => {
    actions.setLeaveDialog(blocker.state === 'blocked')
  }, [actions, blocker.state])

  useEffect(() => {
    if (!dirty) return
    const onBeforeUnload = (event: BeforeUnloadEvent) => {
      event.preventDefault()
      event.returnValue = ''
    }
    window.addEventListener('beforeunload', onBeforeUnload)
    return () => window.removeEventListener('beforeunload', onBeforeUnload)
  }, [dirty])
}
