import { useMutation, useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { ApiError } from '../../api/client'
import { describeApiError } from '../../api/error-text'
import { tp } from '../../i18n'
import { useI18n } from '../../i18n/use-i18n'
import { recoveryCodesQuery, reissueRecoveryCodesMutation, totpDisableMutation, totpEnrollMutation, totpSetupMutation } from '../../query/account'
import { sessionQuery } from '../../query/session'
import { Button } from '../Button'
import { TextField } from '../TextField'
import { SettingsDialog } from './SettingsDialog'

export function TotpSection() {
  const { t } = useI18n()
  const session = useQuery(sessionQuery())
  const enabled = session.data?.user.totp_enabled ?? false
  const smbDedicated = session.data?.user.smb_credential === 'dedicated'
  const recovery = useQuery(recoveryCodesQuery(enabled))
  const setup = useMutation(totpSetupMutation())
  const enroll = useMutation(totpEnrollMutation())
  const disable = useMutation(totpDisableMutation())
  const reissue = useMutation(reissueRecoveryCodesMutation())
  const [enrollOpen, setEnrollOpen] = useState(false)
  const [enrollPassword, setEnrollPassword] = useState('')
  const [setupSecret, setSetupSecret] = useState('')
  const [setupUrl, setSetupUrl] = useState('')
  const [enrollCode, setEnrollCode] = useState('')
  const [secretCopyState, setSecretCopyState] = useState<'idle' | 'copied' | 'failed'>('idle')
  const [recoveryCodes, setRecoveryCodes] = useState<string[] | null>(null)
  const [recoveryAcknowledged, setRecoveryAcknowledged] = useState(false)
  const [recoveryCopyState, setRecoveryCopyState] = useState<'idle' | 'copied' | 'failed'>('idle')
  const [disableOpen, setDisableOpen] = useState(false)
  const [disablePassword, setDisablePassword] = useState('')
  const [reissueOpen, setReissueOpen] = useState(false)
  const [reissuePassword, setReissuePassword] = useState('')

  const enrollError = setup.error || enroll.error
  const enrollErrorText = enrollError ? (enrollError instanceof ApiError && enrollError.code === 'auth.invalid_credentials' ? (setupSecret ? t('totp.password_or_verification_code_incorrect') : t('common.incorrect_password')) : describeApiError(enrollError, setupSecret ? t('totp.could_not_enable_try_again') : t('totp.could_not_start_setup_try'))) : null
  const disableError = disable.error ? (disable.error instanceof ApiError && disable.error.code === 'auth.invalid_credentials' ? t('common.incorrect_password') : describeApiError(disable.error, t('totp.could_not_turn_off_try'))) : null
  const reissueError = reissue.error ? (reissue.error instanceof ApiError && reissue.error.code === 'auth.invalid_credentials' ? t('common.incorrect_password') : describeApiError(reissue.error, t('totp.could_not_reissue_them_try'))) : null

  function openEnroll(): void {
    setEnrollPassword(''); setSetupSecret(''); setSetupUrl(''); setEnrollCode(''); setSecretCopyState('idle'); setup.reset(); enroll.reset(); setEnrollOpen(true)
  }
  function revealSecret(): void {
    if (!enrollPassword || setupSecret) return
    setup.mutate(enrollPassword, { onSuccess: (result) => { setSetupSecret(result.secret); setSetupUrl(result.uri) } })
  }
  function confirmEnroll(): void {
    enroll.mutate({ password: enrollPassword, secret: setupSecret, code: enrollCode }, { onSuccess: (result) => { setRecoveryCodes(result.recovery_codes); setRecoveryAcknowledged(false); setRecoveryCopyState('idle'); setEnrollOpen(false); setSetupSecret(''); setSetupUrl(''); setEnrollPassword(''); setEnrollCode(''); void session.refetch() } })
  }
  function closeEnroll(): void {
    if (setup.isPending || enroll.isPending) return
    setEnrollOpen(false); setSetupSecret(''); setSetupUrl(''); setEnrollPassword(''); setEnrollCode('')
  }
  async function copySecret(): Promise<void> {
    try { await navigator.clipboard.writeText(setupSecret); setSecretCopyState('copied') } catch { setSecretCopyState('failed') }
  }
  async function copyRecoveryCodes(): Promise<void> {
    if (!recoveryCodes) return
    try { await navigator.clipboard.writeText(recoveryCodes.join('\n')); setRecoveryCopyState('copied') } catch { setRecoveryCopyState('failed') }
  }
  function closeRecoveryCodes(): void {
    if (recoveryCodes && !recoveryAcknowledged) return
    setRecoveryCodes(null)
  }
  function acknowledgeRecoveryCodes(): void {
    setRecoveryAcknowledged(true); setRecoveryCodes(null)
  }
  function openDisable(): void { setDisablePassword(''); disable.reset(); setDisableOpen(true) }
  function confirmDisable(): void { disable.mutate(disablePassword, { onSuccess: () => { setDisableOpen(false); setDisablePassword(''); void session.refetch() } }) }
  function openReissue(): void { setReissuePassword(''); reissue.reset(); setReissueOpen(true) }
  function confirmReissue(): void { reissue.mutate(reissuePassword, { onSuccess: (result) => { setRecoveryCodes(result.recovery_codes); setRecoveryAcknowledged(false); setRecoveryCopyState('idle'); setReissueOpen(false); setReissuePassword('') } }) }

  return (
    <div className="sc-totp">
      <div className="sc-totp__status"><span className={enabled ? 'sc-totp__badge sc-totp__badge--on' : 'sc-totp__badge'}>{enabled ? t('totp.on') : t('totp.off')}</span>{enabled ? <Button variant="outlined" onClick={openDisable}>{t('totp.turn_off_two_factor_authentication')}</Button> : <Button onClick={openEnroll}>{t('totp.set_up_two_factor_authentication')}</Button>}</div>
      {enabled ? <div className="sc-totp__recovery">{recovery.data ? <p className={recovery.data.remaining <= 3 ? 'sc-totp__recovery-count sc-totp__recovery-count--low' : 'sc-totp__recovery-count'}>{tp('totp.recovery_codes_left', recovery.data.remaining)}{recovery.data.remaining <= 3 ? ` ${t('totp.running_low_reissue_them_now')}` : ''}</p> : null}<Button variant="outlined" onClick={openReissue}>{t('totp.reissue_recovery_codes')}</Button></div> : null}
      <SettingsDialog open={enrollOpen} title={t('totp.set_up_two_factor_authentication')} onClose={closeEnroll} actions={<><Button variant="text" onClick={closeEnroll} disabled={setup.isPending || enroll.isPending}>{t('common.cancel')}</Button><Button onClick={setupSecret ? confirmEnroll : revealSecret} disabled={setupSecret ? enrollCode.length !== 6 : !enrollPassword} loading={setup.isPending || enroll.isPending}>{setupSecret ? t('totp.enable') : t('common.continue')}</Button></>}>
        <TextField type="password" label={t('common.current_password')} value={enrollPassword} autoComplete="current-password" onValueChange={setEnrollPassword} />
        {setup.isPending ? <p>{t('totp.loading_setup_details')}</p> : null}
        {setupSecret ? <><p>{t('totp.add_key_below_authenticator_app')}</p><div className="sc-totp__secret-row"><input className="sc-totp__secret" readOnly value={setupSecret} aria-label={t('totp.add_key_below_authenticator_app')} /><Button variant="text" onClick={() => void copySecret()}>{secretCopyState === 'copied' ? t('common.copied') : t('common.copy')}</Button></div>{secretCopyState === 'failed' ? <p className="sc-totp__copy-feedback" role="alert">{t('totp.copy_secret_failed')}</p> : null}<p className="sc-totp__url">{setupUrl}</p><TextField label={t('totp.6_digit_code')} value={enrollCode} error={enrollErrorText} onValueChange={setEnrollCode} /></> : enrollErrorText ? <p className="sc-totp__smb-warning" role="alert">{enrollErrorText}</p> : null}
      </SettingsDialog>
      <SettingsDialog open={disableOpen} title={t('totp.turn_off_two_factor_authentication_2')} onClose={() => { if (!disable.isPending) setDisableOpen(false) }} actions={<><Button variant="text" onClick={() => setDisableOpen(false)} disabled={disable.isPending}>{t('common.cancel')}</Button><Button onClick={confirmDisable} disabled={!disablePassword} loading={disable.isPending}>{t('totp.turn_off')}</Button></>}>
        <p>{t('totp.enter_your_current_password_continue')}</p>{smbDedicated ? <p className="sc-totp__smb-warning">{t('smb.dedicated_will_be_replaced')}</p> : <p className="sc-totp__smb-warning">{t('smb.remove_reverts_to_account')}</p>}<TextField type="password" label={t('common.current_password')} value={disablePassword} error={disableError} onValueChange={setDisablePassword} />
      </SettingsDialog>
      <SettingsDialog open={reissueOpen} title={t('totp.reissue_recovery_codes_2')} onClose={() => { if (!reissue.isPending) setReissueOpen(false) }} actions={<><Button variant="text" onClick={() => setReissueOpen(false)} disabled={reissue.isPending}>{t('common.cancel')}</Button><Button onClick={confirmReissue} disabled={!reissuePassword} loading={reissue.isPending}>{t('totp.reissue')}</Button></>}>
        <p>{t('totp.reissuing_invalidates_all_10_recovery')} <strong>{t('totp.at_once')}</strong>{t('totp.cannot_undone_new_codes_shown')}</p><TextField type="password" label={t('common.current_password')} value={reissuePassword} error={reissueError} onValueChange={setReissuePassword} />
      </SettingsDialog>
      <SettingsDialog open={!!recoveryCodes} title={t('totp.recovery_codes')} onClose={closeRecoveryCodes} dismissible={false} actions={<Button onClick={acknowledgeRecoveryCodes}>{t('totp.acknowledge_codes_saved')}</Button>}>
        <p>{t('totp.each_code_works_once_save')}</p><ul className="sc-totp__codes">{(recoveryCodes ?? []).map((code) => <li key={code}><input className="sc-totp__code" readOnly value={code} aria-label={t('totp.recovery_codes')} /></li>)}</ul><Button variant="outlined" onClick={() => void copyRecoveryCodes()}>{recoveryCopyState === 'copied' ? t('common.copied') : t('totp.copy_codes')}</Button>{recoveryCopyState === 'failed' ? <p className="sc-totp__copy-feedback" role="alert">{t('totp.copy_codes_failed')}</p> : null}
      </SettingsDialog>
    </div>
  )
}
