import { useEffect } from 'react'
import { overlay } from 'overlay-kit'
import { useI18n } from '../../hooks/use-i18n'
import { unlock, WrongPassphraseError } from '../../lib/crypto/e2ee'
import { Button } from '../../ui/Button'
import { Dialog } from '../../ui/Dialog'
import { TextField } from '../../ui/TextField'
import { useUnlockShareState } from './hooks/share-manage-state'

interface UnlockShareDialogProps {
  open: boolean
  salt: string
  verifier: string
  onUnlock: () => void
  onClose: () => void
  onClosed?: () => void
}

/** Asks for a share's passphrase and unlocks it. True once unlocked, false when cancelled. */
export function askUnlock(share: { salt: string; verifier: string }): Promise<boolean> {
  return overlay.openAsync<boolean>(({ isOpen, close, unmount }) => (
    <UnlockShareDialog
      salt={share.salt}
      verifier={share.verifier}
      open={isOpen}
      onUnlock={() => close(true)}
      onClose={() => close(false)}
      onClosed={unmount}
    />
  ))
}

function UnlockShareDialog({ open, salt, verifier, onUnlock, onClose, onClosed }: UnlockShareDialogProps) {
  const { t } = useI18n()
  const [form, patch] = useUnlockShareState()
  const { passphrase, unlocking, error } = form

  useEffect(() => {
    if (open) patch({ passphrase: '', error: null })
  }, [open, patch])

  async function submit(): Promise<void> {
    if (!passphrase || unlocking) return
    patch({ unlocking: true, error: null })
    try {
      await unlock(passphrase, salt, verifier)
      patch({ passphrase: '' })
      onUnlock()
    } catch (err) {
      patch({
        error: err instanceof WrongPassphraseError ? t('common.incorrect_password') : t('encryption.could_not_unlock')
      })
    } finally {
      patch({ unlocking: false })
    }
  }

  return (
    <Dialog
      open={open}
      title={t('encryption.unlock_title')}
      role="dialog"
      dismissible={false}
      onClosed={onClosed}
      actions={
        <>
          <Button variant="text" disabled={unlocking} onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button loading={unlocking} disabled={!passphrase} onClick={() => void submit()}>
            {t('encryption.unlock')}
          </Button>
        </>
      }
    >
      <p>{t('encryption.unlock_hint')}</p>
      <form
        onSubmit={(event) => {
          event.preventDefault()
          void submit()
        }}
      >
        <TextField
          type="password"
          label={t('encryption.passphrase')}
          value={passphrase}
          error={error}
          autoComplete="current-password"
          autoFocus={open}
          disabled={unlocking}
          onValueChange={(value) => patch({ passphrase: value })}
        />
      </form>
    </Dialog>
  )
}
