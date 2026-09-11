import { useEffect, useRef, useState } from 'react'
import { useI18n } from '../i18n/use-i18n'
import { unlock, WrongPassphraseError } from '../crypto/e2ee'
import { Button } from './Button'
import { TextField } from './TextField'

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
  const [passphrase, setPassphrase] = useState('')
  const [unlocking, setUnlocking] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!open) return
    setPassphrase('')
    setError(null)
    queueMicrotask(() => (dialogRef.current as unknown as { querySelector?: (s: string) => HTMLElement | null })?.querySelector?.('mdui-text-field')?.focus())
  }, [open])

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
    setUnlocking(true)
    setError(null)
    try {
      await unlock(passphrase, salt, verifier)
      setPassphrase('')
      onUnlock()
    } catch (err) {
      setError(err instanceof WrongPassphraseError ? t('common.incorrect_password') : t('encryption.could_not_unlock'))
    } finally {
      setUnlocking(false)
    }
  }

  return (
    <mdui-dialog ref={dialogRef} headline={t('encryption.unlock_title')} close-on-overlay-click={false} close-on-esc={false}>
      <p>{t('encryption.unlock_hint')}</p>
      <form onSubmit={(event) => { event.preventDefault(); void submit() }}>
        <TextField type="password" label={t('encryption.passphrase')} value={passphrase} error={error} autoComplete="current-password" autoFocus={open} disabled={unlocking} onValueChange={setPassphrase} />
      </form>
      <mdui-button slot="action" variant="text" disabled={unlocking} onClick={onClose}>{t('common.cancel')}</mdui-button>
      <mdui-button slot="action" variant="filled" loading={unlocking} disabled={!passphrase || unlocking} onClick={() => void submit()}>{t('encryption.unlock')}</mdui-button>
    </mdui-dialog>
  )
}
