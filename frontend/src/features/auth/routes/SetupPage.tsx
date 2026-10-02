import { useState } from 'react'
import { Link } from '@tanstack/react-router'
import { PasswordStrengthMeter } from '../components/PasswordStrengthMeter'
import { useI18n } from '../../../hooks/use-i18n'
import { cx, StowButton, StowFormTextField } from '@/shared/ui'
import { pickPath } from '../../file-browser/components/PathPickerDialog'
import { useDocumentTitle } from '../../../hooks/use-document-title'
import { MIN_PASSWORD_LENGTH, setupReady, useSetup } from '../hooks/use-setup'
import * as authStyles from './auth.css'
import { focusRing } from '@/shared/theme'
import type { SetupFinding } from '../api'

const LAST_STEP = 3

function Findings({ findings, role }: { findings: readonly SetupFinding[]; role: 'status' | 'alert' }) {
  const { t } = useI18n()
  if (findings.length === 0) return null
  return (
    <div className={authStyles.warning} role={role}>
      {findings.map((finding, index) => (
        <p key={`${finding.reason}-${index}`} className={authStyles.warningText}>
          {t(finding.reason, finding.args ?? {})}
        </p>
      ))}
    </div>
  )
}

export function SetupPage() {
  const { t } = useI18n()
  const { form, findings, outcome, submit, continueAfterSetup, creating, signingIn, retrying } = useSetup()
  const { control, formState, setValue } = form
  const values = form.watch()
  const [step, setStep] = useState(1)
  const passwordError =
    values.password && values.password.length < MIN_PASSWORD_LENGTH
      ? t('setup.password_must_at_least_characters', { min: MIN_PASSWORD_LENGTH })
      : null
  const confirmError =
    values.passwordConfirm && values.passwordConfirm !== values.password ? t('setup.passwords_do_not_match') : null
  const signedIn = outcome?.signedIn === true
  useDocumentTitle(t('setup.create_administrator_account'))

  // Before sign-in the setup token authorizes the browse.
  const browseShareFolder = async (): Promise<void> => {
    const picked = await pickPath({
      mode: 'folder',
      start: values.sharePath,
      token: signedIn ? undefined : values.token
    })
    if (picked !== null) setValue('sharePath', picked)
  }

  const shareFields = (
    <>
      <StowFormTextField control={control} name="shareName" label={t('common.name')} autoComplete="off" />
      <StowFormTextField
        control={control}
        name="sharePath"
        label={t('folder_share.server_path')}
        autoComplete="off"
        action={
          <StowButton
            variant="outlined"
            disabled={outcome !== null && !signedIn}
            onClick={() => void browseShareFolder()}
          >
            {t('picker.browse_folder')}
          </StowButton>
        }
      />
    </>
  )

  return (
    <main className={authStyles.page}>
      <form className={authStyles.card} onSubmit={(event) => void submit(event)}>
        <h1 className={authStyles.title}>{t('setup.create_administrator_account')}</h1>
        <p className={authStyles.subtitle}>
          {t('setup.on_server_s_first_start')} <code>setup-token</code> {t('setup.file_data_directory')}
        </p>
        {outcome ? (
          <>
            <p className={authStyles.success} role="status">
              {t('setup.account_created')}
            </p>
            {outcome.loginFailed ? (
              <p className={authStyles.error} role="alert">
                <Link to="/login">{t('setup.go_sign_page')}</Link> {t('setup.sign')}
              </p>
            ) : null}
            <Findings findings={outcome.warnings} role="status" />
            {outcome.shareFailed ? (
              <>
                <p className={authStyles.error} role="alert">
                  {t('setup.account_created_share_failed')}
                </p>
                {shareFields}
                {outcome.shareRetryError ? (
                  <p className={authStyles.error} role="alert">
                    {outcome.shareRetryError}
                  </p>
                ) : null}
                {outcome.shareRetryError && signedIn ? (
                  <Link className={cx(authStyles.setupLink, focusRing)} to="/admin/{-$tab}" params={{ tab: 'shares' }}>
                    {t('setup.go_to_admin_shares')}
                  </Link>
                ) : null}
                <div className={authStyles.actions}>
                  <StowButton
                    disabled={retrying || values.shareName.trim().length === 0 || values.sharePath.trim().length === 0}
                    loading={retrying}
                    onClick={() => void continueAfterSetup(outcome)}
                  >
                    {t('setup.retry_first_share')}
                  </StowButton>
                </div>
              </>
            ) : (
              <div className={authStyles.actions}>
                <StowButton loading={signingIn} onClick={() => void continueAfterSetup(outcome)}>
                  {t('setup.continue_anyway')}
                </StowButton>
              </div>
            )}
          </>
        ) : (
          <>
            <ol className={authStyles.steps} aria-label={t('progress.progress')}>
              {[
                t('setup.create_administrator_account'),
                t('setup.how_this_server_is_reached'),
                t('setup.first_shared_folder')
              ].map((label, index) => (
                <li
                  key={label}
                  className={cx(authStyles.step, step === index + 1 && authStyles.stepActive)}
                  aria-current={step === index + 1 ? 'step' : undefined}
                >
                  {index + 1}. {label}
                </li>
              ))}
            </ol>
            {step === 1 ? (
              <>
                <StowFormTextField
                  control={control}
                  name="token"
                  label={t('setup.setup_token')}
                  autoFocus
                  autoComplete="off"
                />
                <StowFormTextField
                  control={control}
                  name="username"
                  label={t('setup.administrator_username')}
                  autoComplete="username"
                />
                <StowFormTextField
                  control={control}
                  name="password"
                  label={t('common.password')}
                  type="password"
                  error={passwordError}
                  autoComplete="new-password"
                />
                <PasswordStrengthMeter
                  password={values.password}
                  label={(level) => t('common.password_strength', { level })}
                />
                <StowFormTextField
                  control={control}
                  name="passwordConfirm"
                  label={t('setup.confirm_password')}
                  type="password"
                  error={confirmError}
                  autoComplete="new-password"
                />
              </>
            ) : null}
            {step === 2 ? (
              <>
                <StowFormTextField
                  control={control}
                  name="appHosts"
                  label={t('server.app_hosts_comma_separated')}
                  autoComplete="off"
                />
                <p className={authStyles.hint}>{t('setup.app_hosts_hint')}</p>
                <StowFormTextField
                  control={control}
                  name="trustedProxies"
                  label={t('server.trusted_proxies_comma_separated')}
                  autoComplete="off"
                />
                <p className={authStyles.hint}>{t('setup.trusted_proxies_hint')}</p>
              </>
            ) : null}
            {step === 3 ? (
              <>
                <p className={authStyles.hint}>{t('setup.first_share_hint')}</p>
                {shareFields}
              </>
            ) : null}
            <Findings findings={findings} role="alert" />
            {formState.errors.root?.message ? (
              <p className={authStyles.error} role="alert">
                {formState.errors.root.message}
              </p>
            ) : null}
            <div className={authStyles.actions}>
              {step > 1 ? (
                <StowButton variant="outlined" type="button" onClick={() => setStep(step - 1)}>
                  {t('common.back')}
                </StowButton>
              ) : null}
              {/* Distinct keys: reusing one element would make the Continue click that reaches the last step submit. */}
              {step < LAST_STEP ? (
                <StowButton key="next" type="button" onClick={() => setStep(step + 1)}>
                  {t('common.continue')}
                </StowButton>
              ) : (
                <StowButton key="create" type="submit" disabled={creating || !setupReady(values)} loading={creating}>
                  {t('setup.create_administrator_account')}
                </StowButton>
              )}
            </div>
            <Link className={cx(authStyles.setupLink, focusRing)} to="/login">
              {t('setup.already_have_account_sign')}
            </Link>
          </>
        )}
      </form>
    </main>
  )
}
