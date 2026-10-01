import { useState } from 'react'
import { describeApiError } from '../../api/error-text'
import { useI18n } from '../../hooks/use-i18n'
import { useSession } from '../auth/api'
import { useClearSmbPassword, useSetSmbPassword, useSmbSettings } from './api'
import { Button } from '../../ui/Button'
import { TextField } from '../../ui/TextField'
import { SettingsDialog } from './SettingsDialog'
import { Switch } from '../../ui/Switch'
import * as styles from './SmbSection.css'
import * as settingsCardStyles from './SettingsCard.css'
import { ApiError } from '../../api/fetcher'

export function SmbSection() {
  const { t } = useI18n()
  const session = useSession()
  const settings = useSmbSettings()
  const setPassword = useSetSmbPassword()
  const clearPassword = useClearSmbPassword()
  const user = session.data?.user
  const credential = user?.smb_credential ?? 'none'
  const optOut = user?.smb_opt_out ?? false
  const enabled = user?.smb_enabled ?? false
  const totpEnabled = user?.totp_enabled ?? false
  const oidcLinked = session.data?.oidc.linked ?? false
  type SmbState = {
    dialog: 'toggle' | 'set' | 'clear' | null
    currentPassword: string
    newPassword: string
    pendingEnabled: boolean
    pendingOptOut: boolean
    error: string | null
    announcement: string
  }
  const [state, setState] = useState<SmbState>({
    dialog: null,
    currentPassword: '',
    newPassword: '',
    pendingEnabled: false,
    pendingOptOut: false,
    error: null,
    announcement: ''
  })
  const { dialog, currentPassword, newPassword, pendingEnabled, pendingOptOut, error, announcement } = state
  const patchState = (patch: Partial<SmbState>): void => setState((current) => ({ ...current, ...patch }))
  const setDialog = (value: SmbState['dialog']): void => patchState({ dialog: value })
  const setCurrentPassword = (value: string): void => patchState({ currentPassword: value })
  const setNewPassword = (value: string): void => patchState({ newPassword: value })
  const setPendingEnabled = (value: boolean): void => patchState({ pendingEnabled: value })
  const setPendingOptOut = (value: boolean): void => patchState({ pendingOptOut: value })
  const setError = (value: string | null): void => patchState({ error: value })
  const setAnnouncement = (value: string): void => patchState({ announcement: value })

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
  const confirmingOptOut = pendingOptOut && !optOut

  function describeMutationError(value: unknown, fallback: string): string {
    if (value instanceof ApiError && value.code === 'auth.invalid_credentials') return t('common.incorrect_password')
    if (value instanceof ApiError && value.code === 'auth.weak_password')
      return t('smb.password_too_short', { min: value.reasonNumber('min_length') ?? 10 })
    if (value instanceof ApiError && value.status === 404) return t('smb.no_separate_password_set')
    return describeApiError(value, fallback)
  }

  function openToggle(nextOptOut: boolean, nextEnabled: boolean): void {
    setPendingOptOut(nextOptOut)
    setPendingEnabled(nextEnabled)
    setCurrentPassword('')
    setError(null)
    settings.reset()
    setDialog('toggle')
  }
  function confirmToggle(): void {
    settings.mutate(
      { currentPassword, enabled: pendingEnabled, optOut: pendingOptOut },
      {
        onSuccess: () => {
          setDialog(null)
          void session.refetch()
        },
        onError: (value) => setError(describeMutationError(value, t('smb.could_not_save_setting_try')))
      }
    )
  }
  function openSet(): void {
    setCurrentPassword('')
    setNewPassword('')
    setError(null)
    setPassword.reset()
    setDialog('set')
  }
  function confirmSet(): void {
    // The server turns the account's own switches back on with the password.
    const togglesCleared = optOut || !enabled
    setPassword.mutate(
      { currentPassword, smbPassword: newPassword },
      {
        onSuccess: () => {
          setDialog(null)
          setAnnouncement(
            togglesCleared ? `${t('smb.password_set')} ${t('smb.toggles_cleared')}` : t('smb.password_set')
          )
          void session.refetch()
        },
        onError: (value) => setError(describeMutationError(value, t('smb.could_not_save_password')))
      }
    )
  }
  function openClear(): void {
    setCurrentPassword('')
    setError(null)
    clearPassword.reset()
    setDialog('clear')
  }
  function confirmClear(): void {
    clearPassword.mutate(currentPassword, {
      onSuccess: (revertible) => {
        setDialog(null)
        setAnnouncement(revertible ? t('smb.password_removed_reverted') : t('smb.password_removed_no_access'))
        void session.refetch()
      },
      onError: (value) => setError(describeMutationError(value, t('smb.could_not_save_password')))
    })
  }

  return (
    <div className={styles.root}>
      <p className={styles.note}>{t('smb.smb_reachable_only_from_local')}</p>
      <p className={styles.state}>{stateLine}</p>
      <div>
        <Switch
          checked={enabled}
          label={t('smb.allow_smb_access')}
          onChange={(checked) => openToggle(optOut, checked)}
        />
      </div>
      <div>
        <Switch
          checked={optOut}
          label={t('smb.do_not_store_smb_credentials')}
          onChange={(checked) => openToggle(checked, checked ? false : enabled)}
        />
      </div>
      <div className={styles.actions}>
        <Button variant={credential === 'dedicated' ? 'outlined' : 'filled'} onClick={openSet}>
          {credential === 'dedicated' ? t('smb.change_separate_password') : t('smb.set_separate_password')}
        </Button>
        {credential === 'dedicated' ? (
          <Button variant="text" onClick={openClear}>
            {t('smb.remove_separate_password')}
          </Button>
        ) : null}
      </div>
      <p className={styles.announce} aria-live="polite">
        {announcement}
      </p>
      <SettingsDialog
        open={dialog === 'toggle'}
        title={confirmingOptOut ? t('smb.confirm_opt_out_title') : t('smb.confirm_setting_title')}
        onClose={() => {
          setCurrentPassword('')
          setError(null)
          setDialog(null)
        }}
        actions={
          <>
            <Button
              variant="text"
              onClick={() => {
                setCurrentPassword('')
                setError(null)
                setDialog(null)
              }}
            >
              {t('common.cancel')}
            </Button>
            <Button onClick={confirmToggle} disabled={!currentPassword} loading={settings.isPending}>
              {confirmingOptOut ? t('smb.confirm_opt_out') : t('common.save')}
            </Button>
          </>
        }
      >
        <p className={settingsCardStyles.text}>
          {confirmingOptOut ? t('smb.confirm_opt_out_warning') : t('smb.confirm_setting_hint')}
        </p>
        <TextField
          type="password"
          label={t('common.current_password')}
          value={currentPassword}
          error={error}
          onValueChange={setCurrentPassword}
        />
      </SettingsDialog>
      <SettingsDialog
        open={dialog === 'set'}
        title={t('smb.set_password_title')}
        onClose={() => setDialog(null)}
        actions={
          <>
            <Button variant="text" onClick={() => setDialog(null)}>
              {t('common.cancel')}
            </Button>
            <Button onClick={confirmSet} disabled={!currentPassword || !newPassword} loading={setPassword.isPending}>
              {t('common.save')}
            </Button>
          </>
        }
      >
        <p className={settingsCardStyles.text}>{t('smb.set_password_hint')}</p>
        <TextField
          type="password"
          label={t('common.current_password')}
          value={currentPassword}
          onValueChange={setCurrentPassword}
        />
        <TextField
          type="password"
          label={t('smb.new_smb_password')}
          value={newPassword}
          error={error}
          onValueChange={setNewPassword}
        />
      </SettingsDialog>
      <SettingsDialog
        open={dialog === 'clear'}
        title={t('smb.remove_password_title')}
        onClose={() => setDialog(null)}
        actions={
          <>
            <Button variant="text" onClick={() => setDialog(null)}>
              {t('common.cancel')}
            </Button>
            <Button onClick={confirmClear} disabled={!currentPassword} loading={clearPassword.isPending}>
              {t('common.remove_2')}
            </Button>
          </>
        }
      >
        <p className={settingsCardStyles.text}>
          {revertsToAccount ? t('smb.remove_reverts_to_account') : t('smb.remove_ends_access')}
        </p>
        <TextField
          type="password"
          label={t('common.current_password')}
          value={currentPassword}
          error={error}
          onValueChange={setCurrentPassword}
        />
      </SettingsDialog>
    </div>
  )
}
