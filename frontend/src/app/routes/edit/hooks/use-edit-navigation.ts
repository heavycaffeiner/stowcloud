import { useEffect } from 'react'
import { useBeforeUnload } from '../../../../hooks/use-before-unload'
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

  useBeforeUnload(dirty)
}
