import { useEffect } from 'react'
import { describeApiError } from '../../../../lib/api/error-text'
import { openSessionValue, sealSessionValue } from '../../../../lib/crypto/e2ee'
import { type EditActions, type SealedDraft } from './use-edit-state'

export interface EditSessionLockOptions {
  path: string
  entryPath?: string
  shareSalt?: string
  dirty: boolean
  draft: string | null
  sealedDraft: SealedDraft | null
  actions: Pick<EditActions, 'sealDraft' | 'restoreDraft' | 'setDraft' | 'setUnlockRequested' | 'setConflict' | 'setLeaveDialog' | 'setSnackbar'>
  removeContent: (path: string) => void
  translate: (key: string) => string
}

export function useEditSessionLock({ path, entryPath, shareSalt, dirty, draft, sealedDraft, actions, removeContent, translate }: EditSessionLockOptions): void {
  useEffect(() => {
    const beforeLock = (event: Event) => {
      const salt = (event as CustomEvent<{ salt?: string }>).detail?.salt
      if (!dirty || draft === null || !salt || shareSalt !== salt) return
      const plaintext = new TextEncoder().encode(draft)
      try {
        actions.sealDraft({ salt, bytes: sealSessionValue(plaintext, salt) })
      } catch (error) {
        actions.setSnackbar(describeApiError(error, translate('common.could_not_save')))
      } finally {
        plaintext.fill(0)
      }
    }

    const onUnlock = (event: Event) => {
      const salt = (event as CustomEvent<{ salt?: string }>).detail?.salt
      if (!salt || sealedDraft?.salt !== salt) return
      let plaintext: Uint8Array | null = null
      try {
        plaintext = openSessionValue(sealedDraft.bytes, salt)
        actions.restoreDraft(new TextDecoder().decode(plaintext))
      } catch (error) {
        actions.setSnackbar(describeApiError(error, translate('editor.could_not_load_file')))
      } finally {
        plaintext?.fill(0)
      }
    }

    const onLock = () => {
      actions.setUnlockRequested(sealedDraft === null)
      actions.setConflict(false)
      actions.setLeaveDialog(false)
      removeContent(entryPath ?? path)
    }

    window.addEventListener('sc:before-lock', beforeLock)
    window.addEventListener('sc:unlock', onUnlock)
    window.addEventListener('sc:lock', onLock)
    return () => {
      window.removeEventListener('sc:before-lock', beforeLock)
      window.removeEventListener('sc:unlock', onUnlock)
      window.removeEventListener('sc:lock', onLock)
    }
  }, [actions, dirty, draft, entryPath, path, removeContent, sealedDraft, shareSalt, translate])
}
