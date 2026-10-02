import { useBlocker } from '@tanstack/react-router'
import { askLeaveEditor } from '../components/EditDialogs'

export interface EditNavigationOptions {
  name: string
  dirty: boolean
  canSave: boolean
  save: () => Promise<boolean>
  discard: () => void
}

/**
 * Holds a navigation away from unsaved changes until the user decides what happens to them.
 * Closing or reloading the tab gets the browser's own prompt.
 */
export function useEditNavigation({ name, dirty, canSave, save, discard }: EditNavigationOptions): void {
  useBlocker({
    shouldBlockFn: async ({ current, next }) => {
      if (current.pathname === next.pathname) return false
      const choice = await askLeaveEditor(name, canSave)
      if (choice === 'discard') {
        discard()
        return false
      }
      return !(choice === 'save' && (await save()))
    },
    disabled: !dirty
  })
}
