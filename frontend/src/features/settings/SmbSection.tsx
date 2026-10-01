import { useState } from 'react'
import { overlay } from 'overlay-kit'
import { useForm, useWatch } from 'react-hook-form'
import { describeApiError } from '../../api/error-text'
import { t } from '../../lib/i18n'
import { useI18n } from '../../hooks/use-i18n'
import { useSession } from '../auth/api'
import { useClearSmbPassword, useSetSmbPassword, useSmbSettings } from './api'
import { Button } from '../../ui/Button'
import { FormTextField } from '../../ui/FormTextField'
import { askPassword } from './PasswordPrompt'
import { SettingsDialog } from './SettingsDialog'
import { Switch } from '../../ui/Switch'
import * as styles from './SmbSection.css'
import * as settingsCardStyles from './SettingsCard.css'
import { ApiError } from '../../api/fetcher'

function smbErrorText(error: unknown, fallback: string): string {
  if (error instanceof ApiError && error.code === 'auth.invalid_credentials') return t('common.incorrect_password')
  if (error instanceof ApiError && error.code === 'auth.weak_password')
    return t('smb.password_too_short', { min: error.reasonNumber('min_length') ?? 10 })
  if (error instanceof ApiError && error.status === 404) return t('smb.no_separate_password_set')
  return describeApiError(error, fallback)
}

export function SmbSection() {
  const { t } = useI18n()
  const session = useSession()
  const settings = useSmbSettings()
  const clearPassword = useClearSmbPassword()
  const [announcement, setAnnouncement] = useState('')
  const user = session.data?.user
  const credential = user?.smb_credential ?? 'none'
  const optOut = user?.smb_opt_out ?? false
  const enabled = user?.smb_enabled ?? false
  const totpEnabled = user?.totp_enabled ?? false
  const oidcLinked = session.data?.oidc.linked ?? false

  const stateLine =
    credential === 'account'
      ? t('smb.uses_account_password')
      : credential === 'dedicated'
        ? t('smb.uses_separate_password')
        : user?.smb_unavailable_reason === 'totp_blocked'
          ? t('smb.unavailable_totp_blocked')
          : optOut
            ? t('smb.unavailable_opted_out')
            : !enabled
              ? t('smb.unavailable_not_published')
              : totpEnabled || oidcLinked
                ? t('smb.unavailable_not_set')
                : t('smb.unavailable_awaiting_sign_in')
  const revertsToAccount = !totpEnabled && !oidcLinked && !optOut && enabled

  function toggle(nextOptOut: boolean, nextEnabled: boolean): void {
    const confirmingOptOut = nextOptOut && !optOut
    void askPassword({
      title: confirmingOptOut ? t('smb.confirm_opt_out_title') : t('smb.confirm_setting_title'),
      action: confirmingOptOut ? t('smb.confirm_opt_out') : t('common.save'),
      body: (
        <p className={settingsCardStyles.text}>
          {confirmingOptOut ? t('smb.confirm_opt_out_warning') : t('smb.confirm_setting_hint')}
        </p>
      ),
      run: (currentPassword) => settings.mutateAsync({ currentPassword, enabled: nextEnabled, optOut: nextOptOut }),
      describeError: (error) => smbErrorText(error, t('smb.could_not_save_setting_try'))
    })
  }
  async function setSeparate(): Promise<void> {
    // The server turns the account's own switches back on with the password.
    const togglesCleared = optOut || !enabled
    if (!(await askSmbPassword())) return
    setAnnouncement(togglesCleared ? `${t('smb.password_set')} ${t('smb.toggles_cleared')}` : t('smb.password_set'))
  }
  async function clearSeparate(): Promise<void> {
    const revertible = await askPassword({
      title: t('smb.remove_password_title'),
      action: t('common.remove_2'),
      body: (
        <p className={settingsCardStyles.text}>
          {revertsToAccount ? t('smb.remove_reverts_to_account') : t('smb.remove_ends_access')}
        </p>
      ),
      run: (currentPassword) => clearPassword.mutateAsync(currentPassword),
      describeError: (error) => smbErrorText(error, t('smb.could_not_save_password'))
    })
    if (revertible === null) return
    setAnnouncement(revertible ? t('smb.password_removed_reverted') : t('smb.password_removed_no_access'))
  }

  return (
    <div className={styles.root}>
      <p className={styles.note}>{t('smb.smb_reachable_only_from_local')}</p>
      <p className={styles.state}>{stateLine}</p>
      <div>
        <Switch checked={enabled} label={t('smb.allow_smb_access')} onChange={(checked) => toggle(optOut, checked)} />
      </div>
      <div>
        <Switch
          checked={optOut}
          label={t('smb.do_not_store_smb_credentials')}
          onChange={(checked) => toggle(checked, checked ? false : enabled)}
        />
      </div>
      <div className={styles.actions}>
        <Button variant={credential === 'dedicated' ? 'outlined' : 'filled'} onClick={() => void setSeparate()}>
          {credential === 'dedicated' ? t('smb.change_separate_password') : t('smb.set_separate_password')}
        </Button>
        {credential === 'dedicated' ? (
          <Button variant="text" onClick={() => void clearSeparate()}>
            {t('smb.remove_separate_password')}
          </Button>
        ) : null}
      </div>
      <p className={styles.announce} aria-live="polite">
        {announcement}
      </p>
    </div>
  )
}

/** Resolves true once a separate SMB password is stored, false when cancelled. */
function askSmbPassword(): Promise<boolean> {
  return overlay.openAsync<boolean>(({ isOpen, close, unmount }) => (
    <SmbPasswordDialog open={isOpen} onDone={close} onClosed={unmount} />
  ))
}

interface SmbPasswordDialogProps {
  open: boolean
  onDone: (saved: boolean) => void
  onClosed: () => void
}

function SmbPasswordDialog({ open, onDone, onClosed }: SmbPasswordDialogProps) {
  const { t } = useI18n()
  const save = useSetSmbPassword()
  const { control, handleSubmit } = useForm({ defaultValues: { currentPassword: '', smbPassword: '' } })
  const [currentPassword, smbPassword] = useWatch({ control, name: ['currentPassword', 'smbPassword'] })
  const submit = handleSubmit((values) => {
    if (values.currentPassword && values.smbPassword && !save.isPending)
      save.mutate(values, { onSuccess: () => onDone(true) })
  })
  const cancel = (): void => {
    if (!save.isPending) onDone(false)
  }
  return (
    <SettingsDialog
      open={open}
      title={t('smb.set_password_title')}
      onClose={cancel}
      onClosed={onClosed}
      onSubmit={(event) => void submit(event)}
      actions={
        <>
          <Button variant="text" disabled={save.isPending} onClick={cancel}>
            {t('common.cancel')}
          </Button>
          <Button disabled={!currentPassword || !smbPassword} loading={save.isPending} onClick={() => void submit()}>
            {t('common.save')}
          </Button>
        </>
      }
    >
      <p className={settingsCardStyles.text}>{t('smb.set_password_hint')}</p>
      <FormTextField
        control={control}
        name="currentPassword"
        type="password"
        label={t('common.current_password')}
        autoComplete="current-password"
      />
      <FormTextField
        control={control}
        name="smbPassword"
        type="password"
        label={t('smb.new_smb_password')}
        autoComplete="new-password"
        error={save.error ? smbErrorText(save.error, t('smb.could_not_save_password')) : null}
      />
    </SettingsDialog>
  )
}
