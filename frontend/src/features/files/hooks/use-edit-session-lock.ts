import { useEventListener } from '../../../hooks/use-event-listener'
import { describeApiError } from '../../../api/error-text'
import { openSessionValue, sealSessionValue } from '../../../lib/crypto/e2ee'
import { type EditActions, type SealedDraft } from './use-edit-state'

export interface EditSessionLockOptions {
  path: string
  entryPath?: string
  shareSalt?: string
  dirty: boolean
  draft: string | null
  sealedDraft: SealedDraft | null
  actions: Pick<
    EditActions,
    'sealDraft' | 'restoreDraft' | 'setDraft' | 'setUnlockRequested' | 'setConflict' | 'setLeaveDialog' | 'setSnackbar'
  >
  removeContent: (path: string) => void
  translate: (key: string) => string
}

export function useEditSessionLock({
  path,
  entryPath,
  shareSalt,
  dirty,
  draft,
  sealedDraft,
  actions,
  removeContent,
  translate
}: EditSessionLockOptions): void {
  useEventListener(window, 'sc:before-lock', (event) => {
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
  })

  useEventListener(window, 'sc:unlock', (event) => {
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
  })

  useEventListener(window, 'sc:lock', () => {
    actions.setUnlockRequested(sealedDraft === null)
    actions.setConflict(false)
    actions.setLeaveDialog(false)
    removeContent(entryPath ?? path)
  })
}
