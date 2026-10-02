import { overlay } from 'overlay-kit'
import { useForm, useWatch } from 'react-hook-form'
import { describeApiError } from '../../../api/error-text'
import { tp } from '../../../i18n'
import { useCopyText } from '../../../hooks/use-copy-text'
import { useI18n } from '../../../hooks/use-i18n'
import { useReissueRecoveryCodes, useRecoveryCodesRemaining, useTotpDisable, useTotpEnroll, useTotpSetup } from '../api'
import { useSession } from '../../auth/api'
import { cx, StowBadge, StowButton, StowFormTextField, StowTextField } from '@/shared/ui'
import { askPassword } from './PasswordPrompt'
import { SettingsDialog } from './SettingsDialog'
import * as styles from './TotpSection.css'
import * as settingsCardStyles from './SettingsCard.css'
import { ApiError } from '../../../api/fetcher'

export function TotpSection() {
  const { t } = useI18n()
  const session = useSession()
  const enabled = session.data?.user.totp_enabled ?? false
  const smbDedicated = session.data?.user.smb_credential === 'dedicated'
  const recovery = useRecoveryCodesRemaining(enabled)
  const disable = useTotpDisable()
  const reissue = useReissueRecoveryCodes()

  async function setUp(): Promise<void> {
    const codes = await enrollTotp()
    if (codes) await showRecoveryCodes(codes)
  }
  function turnOff(): void {
    void askPassword({
      title: t('totp.turn_off_two_factor_authentication_2'),
      action: t('totp.turn_off'),
      body: (
        <>
          <p className={settingsCardStyles.text}>{t('totp.enter_your_current_password_continue')}</p>
          <p className={settingsCardStyles.warning}>
            {smbDedicated ? t('smb.dedicated_will_be_replaced') : t('smb.remove_reverts_to_account')}
          </p>
        </>
      ),
      run: (password) => disable.mutateAsync(password),
      describeError: (error) => describeApiError(error, t('totp.could_not_turn_off_try'))
    })
  }
  async function reissueCodes(): Promise<void> {
    const codes = await askPassword({
      title: t('totp.reissue_recovery_codes_2'),
      action: t('totp.reissue'),
      body: (
        <p className={settingsCardStyles.text}>
          {t('totp.reissuing_invalidates_all_10_recovery')} <strong>{t('totp.at_once')}</strong>
          {t('totp.cannot_undone_new_codes_shown')}
        </p>
      ),
      run: (password) => reissue.mutateAsync(password),
      describeError: (error) => describeApiError(error, t('totp.could_not_reissue_them_try'))
    })
    if (codes) await showRecoveryCodes(codes)
  }

  return (
    <div className={settingsCardStyles.section}>
      <div className={settingsCardStyles.cluster}>
        <StowBadge tone={enabled ? 'accent' : 'neutral'}>{enabled ? t('totp.on') : t('totp.off')}</StowBadge>
        {enabled ? (
          <StowButton variant="outlined" onClick={turnOff}>
            {t('totp.turn_off_two_factor_authentication')}
          </StowButton>
        ) : (
          <StowButton onClick={() => void setUp()}>{t('totp.set_up_two_factor_authentication')}</StowButton>
        )}
      </div>
      {enabled ? (
        <div className={settingsCardStyles.cluster}>
          {recovery.data !== undefined ? (
            <p className={cx(styles.recoveryCount, recovery.data <= 3 && styles.recoveryCountLow)}>
              {tp('totp.recovery_codes_left', recovery.data)}
              {recovery.data <= 3 ? ` ${t('totp.running_low_reissue_them_now')}` : ''}
            </p>
          ) : null}
          <StowButton variant="outlined" onClick={() => void reissueCodes()}>
            {t('totp.reissue_recovery_codes')}
          </StowButton>
        </div>
      ) : null}
    </div>
  )
}

/** Walks through enrolling a second factor. Resolves to the recovery codes, or null when cancelled. */
function enrollTotp(): Promise<string[] | null> {
  return overlay.openAsync<string[] | null>(({ isOpen, close, unmount }) => (
    <EnrollDialog open={isOpen} onDone={close} onClosed={unmount} />
  ))
}

interface EnrollDialogProps {
  open: boolean
  onDone: (codes: string[] | null) => void
  onClosed: () => void
}

function EnrollDialog({ open, onDone, onClosed }: EnrollDialogProps) {
  const { t } = useI18n()
  const setup = useTotpSetup()
  const enroll = useTotpEnroll()
  const copy = useCopyText()
  const { control, handleSubmit } = useForm({ defaultValues: { password: '', code: '' } })
  const [password, code] = useWatch({ control, name: ['password', 'code'] })
  const secret = setup.data?.secret ?? ''
  const busy = setup.isPending || enroll.isPending
  const error = setup.error ?? enroll.error
  const errorText = error
    ? error instanceof ApiError && error.code === 'auth.invalid_credentials'
      ? secret
        ? t('totp.password_or_verification_code_incorrect')
        : t('common.incorrect_password')
      : describeApiError(error, secret ? t('totp.could_not_enable_try_again') : t('totp.could_not_start_setup_try'))
    : null

  // The password reveals a secret first; the code from the app then enrolls it.
  const submit = handleSubmit((values) => {
    if (busy) return
    if (!secret) {
      if (values.password) setup.mutate(values.password)
    } else if (values.code.length === 6) {
      enroll.mutate({ password: values.password, secret, code: values.code }, { onSuccess: onDone })
    }
  })
  const cancel = (): void => {
    if (!busy) onDone(null)
  }

  return (
    <SettingsDialog
      open={open}
      title={t('totp.set_up_two_factor_authentication')}
      onClose={cancel}
      onClosed={onClosed}
      onSubmit={(event) => void submit(event)}
      actions={
        <>
          <StowButton variant="text" onClick={cancel} disabled={busy}>
            {t('common.cancel')}
          </StowButton>
          <StowButton onClick={() => void submit()} disabled={secret ? code.length !== 6 : !password} loading={busy}>
            {secret ? t('totp.enable') : t('common.continue')}
          </StowButton>
        </>
      }
    >
      <StowFormTextField
        control={control}
        name="password"
        type="password"
        label={t('common.current_password')}
        autoComplete="current-password"
      />
      {setup.isPending ? <p className={settingsCardStyles.text}>{t('totp.loading_setup_details')}</p> : null}
      {secret ? (
        <>
          <p className={settingsCardStyles.text}>{t('totp.add_key_below_authenticator_app')}</p>
          <div className={settingsCardStyles.copyRow}>
            <StowTextField readOnly value={secret} aria-label={t('totp.add_key_below_authenticator_app')} />
            <StowButton variant="text" onClick={() => copy.mutate(secret)}>
              {copy.isSuccess ? t('common.copied') : t('common.copy')}
            </StowButton>
          </div>
          {copy.isError ? (
            <p className={settingsCardStyles.error} role="alert">
              {t('totp.copy_secret_failed')}
            </p>
          ) : null}
          <p className={settingsCardStyles.muted}>{setup.data?.uri}</p>
          <StowFormTextField control={control} name="code" label={t('totp.6_digit_code')} error={errorText} />
        </>
      ) : errorText ? (
        <p className={settingsCardStyles.warning} role="alert">
          {errorText}
        </p>
      ) : null}
    </SettingsDialog>
  )
}

/** Shows freshly issued recovery codes until the user says they are saved. */
function showRecoveryCodes(codes: string[]): Promise<void> {
  return overlay.openAsync<void>(({ isOpen, close, unmount }) => (
    <RecoveryCodesDialog open={isOpen} codes={codes} onDone={() => close()} onClosed={unmount} />
  ))
}

interface RecoveryCodesDialogProps {
  open: boolean
  codes: string[]
  onDone: () => void
  onClosed: () => void
}

function RecoveryCodesDialog({ open, codes, onDone, onClosed }: RecoveryCodesDialogProps) {
  const { t } = useI18n()
  const copy = useCopyText()
  return (
    <SettingsDialog
      open={open}
      title={t('totp.recovery_codes')}
      onClosed={onClosed}
      dismissible={false}
      actions={<StowButton onClick={onDone}>{t('totp.acknowledge_codes_saved')}</StowButton>}
    >
      <p className={settingsCardStyles.text}>{t('totp.each_code_works_once_save')}</p>
      <ul className={styles.codes}>
        {codes.map((code) => (
          <li key={code}>
            <StowTextField readOnly value={code} aria-label={t('totp.recovery_codes')} />
          </li>
        ))}
      </ul>
      <StowButton variant="outlined" onClick={() => copy.mutate(codes.join('\n'))}>
        {copy.isSuccess ? t('common.copied') : t('totp.copy_codes')}
      </StowButton>
      {copy.isError ? (
        <p className={settingsCardStyles.error} role="alert">
          {t('totp.copy_codes_failed')}
        </p>
      ) : null}
    </SettingsDialog>
  )
}
