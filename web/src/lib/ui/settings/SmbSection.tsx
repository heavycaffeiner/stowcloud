import { useMutation, useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { ApiError } from '../../api/client'
import { describeApiError } from '../../api/error-text'
import { useI18n } from '../../i18n/use-i18n'
import { sessionQuery } from '../../query/session'
import { smbPasswordMutation, smbSettingsMutation } from '../../query/account'
import { Button } from '../Button'
import { TextField } from '../TextField'
import { SettingsDialog } from './SettingsDialog'
import { Switch } from '../Switch'

export function SmbSection() {
  const { t } = useI18n()
  const session = useQuery(sessionQuery())
  const settings = useMutation(smbSettingsMutation())
  const setPassword = useMutation(smbPasswordMutation())
  const clearPassword = useMutation(smbPasswordMutation())
  const user = session.data?.user
  const credential = user?.smb_credential ?? 'none'
  const optOut = user?.smb_opt_out ?? false
  const enabled = user?.smb_enabled ?? false
  const totpEnabled = user?.totp_enabled ?? false
  const oidcLinked = session.data?.oidc.linked ?? false
  const [dialog, setDialog] = useState<'toggle' | 'set' | 'clear' | null>(null)
  const [currentPassword, setCurrentPassword] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [pendingEnabled, setPendingEnabled] = useState(false)
  const [pendingOptOut, setPendingOptOut] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [announcement, setAnnouncement] = useState('')

  const stateLine = credential === 'account'
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
    if (value instanceof ApiError && value.code === 'auth.weak_password') return t('smb.password_too_short', { min: value.reasonNumber('min_length') ?? 10 })
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
    settings.mutate({ currentPassword, enabled: pendingEnabled, optOut: pendingOptOut }, { onSuccess: () => { setDialog(null); void session.refetch() }, onError: (value) => setError(describeMutationError(value, t('smb.could_not_save_setting_try'))) })
  }
  function openSet(): void {
    setCurrentPassword('')
    setNewPassword('')
    setError(null)
    setPassword.reset()
    setDialog('set')
  }
  function confirmSet(): void {
    setPassword.mutate({ currentPassword, smbPassword: newPassword }, {
      onSuccess: (result) => {
        setDialog(null)
        setAnnouncement('smb_toggles_cleared' in result && result.smb_toggles_cleared ? `${t('smb.password_set')} ${t('smb.toggles_cleared')}` : t('smb.password_set'))
        void session.refetch()
      },
      onError: (value) => setError(describeMutationError(value, t('smb.could_not_save_password')))
    })
  }
  function openClear(): void {
    setCurrentPassword('')
    setError(null)
    clearPassword.reset()
    setDialog('clear')
  }
  function confirmClear(): void {
    clearPassword.mutate({ currentPassword, smbPassword: null }, {
      onSuccess: (result) => {
        setDialog(null)
        setAnnouncement('reverted_to_account_password' in result && result.reverted_to_account_password ? t('smb.password_removed_reverted') : t('smb.password_removed_no_access'))
        void session.refetch()
      },
      onError: (value) => setError(describeMutationError(value, t('smb.could_not_save_password')))
    })
  }

  return (
    <div className="sc-smb">
      <p className="sc-smb__note">{t('smb.smb_reachable_only_from_local')}</p>
      <p className="sc-smb__state">{stateLine}</p>
      <div className="sc-smb__row"><Switch checked={enabled} label={t('smb.allow_smb_access')} onChange={(checked) => openToggle(optOut, checked)} /></div>
      <div className="sc-smb__row"><Switch checked={optOut} label={t('smb.do_not_store_smb_credentials')} onChange={(checked) => openToggle(checked, checked ? false : enabled)} /></div>
      <div className="sc-smb__actions">
        <Button variant={credential === 'dedicated' ? 'outlined' : 'filled'} onClick={openSet}>{credential === 'dedicated' ? t('smb.change_separate_password') : t('smb.set_separate_password')}</Button>
        {credential === 'dedicated' ? <Button variant="text" onClick={openClear}>{t('smb.remove_separate_password')}</Button> : null}
      </div>
      <p className="sc-smb__announce" aria-live="polite">{announcement}</p>
      <SettingsDialog open={dialog === 'toggle'} title={confirmingOptOut ? t('smb.confirm_opt_out_title') : t('smb.confirm_setting_title')} onClose={() => setDialog(null)} actions={<><Button variant="text" onClick={() => setDialog(null)}>{t('common.cancel')}</Button><Button onClick={confirmToggle} disabled={!currentPassword} loading={settings.isPending}>{confirmingOptOut ? t('smb.confirm_opt_out') : t('common.save')}</Button></>}>
        <p>{confirmingOptOut ? t('smb.confirm_opt_out_warning') : t('smb.confirm_setting_hint')}</p><TextField type="password" label={t('common.current_password')} value={currentPassword} error={error} onValueChange={setCurrentPassword} />
      </SettingsDialog>
      <SettingsDialog open={dialog === 'set'} title={t('smb.set_password_title')} onClose={() => setDialog(null)} actions={<><Button variant="text" onClick={() => setDialog(null)}>{t('common.cancel')}</Button><Button onClick={confirmSet} disabled={!currentPassword || !newPassword} loading={setPassword.isPending}>{t('common.save')}</Button></>}>
        <p>{t('smb.set_password_hint')}</p><TextField type="password" label={t('common.current_password')} value={currentPassword} onValueChange={setCurrentPassword} /><TextField type="password" label={t('smb.new_smb_password')} value={newPassword} error={error} onValueChange={setNewPassword} />
      </SettingsDialog>
      <SettingsDialog open={dialog === 'clear'} title={t('smb.remove_password_title')} onClose={() => setDialog(null)} actions={<><Button variant="text" onClick={() => setDialog(null)}>{t('common.cancel')}</Button><Button onClick={confirmClear} disabled={!currentPassword} loading={clearPassword.isPending}>{t('common.remove_2')}</Button></>}>
        <p>{revertsToAccount ? t('smb.remove_reverts_to_account') : t('smb.remove_ends_access')}</p><TextField type="password" label={t('common.current_password')} value={currentPassword} error={error} onValueChange={setCurrentPassword} />
      </SettingsDialog>
    </div>
  )
}
