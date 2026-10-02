import { overlay } from 'overlay-kit'
import { useForm, useWatch } from 'react-hook-form'
import { useI18n } from '../../../hooks/use-i18n'
import { WrongPassphraseError } from '../../../lib/crypto/e2ee'
import { Button } from '../../../ui/Button'
import { Dialog } from '../../../ui/Dialog'
import { FormTextField } from '../../../ui/FormTextField'
import { unlockShare } from '../e2ee-store'

interface UnlockShareDialogProps {
  open: boolean
  salt: string
  verifier: string
  onUnlock: () => void
  onClose: () => void
  onClosed: () => void
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
  const { control, handleSubmit, setError, formState } = useForm({ defaultValues: { passphrase: '' } })
  const passphrase = useWatch({ control, name: 'passphrase' })
  const unlocking = formState.isSubmitting
  const submit = handleSubmit(async ({ passphrase }) => {
    try {
      await unlockShare(passphrase, salt, verifier)
      onUnlock()
    } catch (error) {
      const message =
        error instanceof WrongPassphraseError ? t('common.incorrect_password') : t('encryption.could_not_unlock')
      setError('passphrase', { message })
    }
  })

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
      <form onSubmit={(event) => void submit(event)}>
        <FormTextField
          control={control}
          name="passphrase"
          rules={{ required: true }}
          type="password"
          label={t('encryption.passphrase')}
          autoComplete="current-password"
          autoFocus={open}
          disabled={unlocking}
        />
      </form>
    </Dialog>
  )
}
