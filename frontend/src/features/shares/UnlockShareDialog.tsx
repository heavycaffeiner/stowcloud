import { useEffect, useRef } from 'react'
import { useI18n } from '../../hooks/use-i18n'
import { unlock, WrongPassphraseError } from '../../lib/crypto/e2ee'
import { Button } from '../../lib/ui/Button'
import { TextField } from '../../lib/ui/TextField'
import { useUnlockShareState } from './hooks/share-manage-state'

export interface UnlockShareDialogProps {
  open: boolean
  salt: string
  verifier: string
  onUnlock: () => void
  onClose: () => void
}

export function UnlockShareDialog({ open, salt, verifier, onUnlock, onClose }: UnlockShareDialogProps) {
  const { t } = useI18n()
  const dialogRef = useRef<HTMLElement>(null)
  const [form, patch] = useUnlockShareState()
  const { passphrase, unlocking, error } = form

  useEffect(() => {
    if (!open) return
    patch({ passphrase: '', error: null })
    queueMicrotask(() => (dialogRef.current as unknown as { querySelector?: (s: string) => HTMLElement | null })?.querySelector?.('mdui-text-field')?.focus())
  }, [open, patch])

  useEffect(() => {
    const dialog = dialogRef.current as unknown as { open?: boolean; addEventListener: typeof window.addEventListener; removeEventListener: typeof window.removeEventListener } | null
    if (!dialog) return
    dialog.open = open
    const close = () => {
      if (open) onClose()
    }
    dialog.addEventListener('close', close)
    return () => dialog.removeEventListener('close', close)
  }, [open, onClose])

  async function submit(): Promise<void> {
    if (!passphrase || unlocking) return
    patch({ unlocking: true, error: null })
    try {
      await unlock(passphrase, salt, verifier)
      patch({ passphrase: '' })
      onUnlock()
    } catch (err) {
      patch({ error: err instanceof WrongPassphraseError ? t('common.incorrect_password') : t('encryption.could_not_unlock') })
    } finally {
      patch({ unlocking: false })
    }
  }

  return (
    <mdui-dialog ref={dialogRef} headline={t('encryption.unlock_title')} close-on-overlay-click={false} close-on-esc={false}>
      <p>{t('encryption.unlock_hint')}</p>
      <form onSubmit={(event) => { event.preventDefault(); void submit() }}>
        <TextField type="password" label={t('encryption.passphrase')} value={passphrase} error={error} autoComplete="current-password" autoFocus={open} disabled={unlocking} onValueChange={(value) => patch({ passphrase: value })} />
      </form>
      <mdui-button slot="action" variant="text" disabled={unlocking} onClick={onClose}>{t('common.cancel')}</mdui-button>
      <mdui-button slot="action" variant="filled" loading={unlocking} disabled={!passphrase || unlocking} onClick={() => void submit()}>{t('encryption.unlock')}</mdui-button>
    </mdui-dialog>
  )
}
