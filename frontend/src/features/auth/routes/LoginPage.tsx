import { useWatch } from 'react-hook-form'
import { Link } from '@tanstack/react-router'
import { cx, StowButton, StowFormTextField } from '@/shared/ui'
import { useDocumentTitle } from '../../../hooks/use-document-title'
import { useI18n } from '../../../hooks/use-i18n'
import { useLoginFlow } from '../hooks/use-login-flow'
import * as authStyles from './auth.css'
import { focusRing } from '@/shared/theme'

const SOURCE_URL = 'https://github.com/heavycaffeiner/stowcloud'

export function LoginPage() {
  const { t } = useI18n()
  const {
    form,
    step,
    factorMode,
    pending,
    returnTo,
    ssoName,
    ssoError,
    submit,
    switchFactor,
    backToPassword,
    startOidcLogin
  } = useLoginFlow()
  const { control, formState } = form
  const [username, password, code] = useWatch({ control, name: ['username', 'password', 'code'] })
  const errorMessage = formState.errors.root?.message
  useDocumentTitle(t('login.sign_stowcloud'))
  return (
    <main className={authStyles.page}>
      <form className={cx(authStyles.card, authStyles.login)} onSubmit={(event) => void submit(event)}>
        <h1 className={authStyles.title}>Stowcloud</h1>
        <p className={authStyles.subtitle}>
          {returnTo ? t('login.sign_first_authorise_app') : t('login.sign_your_account')}
        </p>
        {step === 'credentials' ? (
          <>
            <StowFormTextField
              control={control}
              name="username"
              label={t('login.username')}
              autoFocus
              autoComplete="username"
            />
            <StowFormTextField
              control={control}
              name="password"
              label={t('common.password')}
              type="password"
              autoComplete="current-password"
            />
          </>
        ) : (
          <>
            <p className={authStyles.subtitle}>
              {factorMode === 'totp' ? t('login.enter_your_two_factor_code') : t('login.recovery_code_hint')}
            </p>
            <StowFormTextField
              control={control}
              name="code"
              label={factorMode === 'totp' ? t('login.verification_code') : t('login.recovery_code')}
              placeholder={factorMode === 'totp' ? t('login.6_digits') : undefined}
              autoFocus
              autoComplete="one-time-code"
            />
            <StowButton variant="text" onClick={switchFactor}>
              {factorMode === 'totp' ? t('login.use_recovery_code') : t('login.use_authenticator_code')}
            </StowButton>
          </>
        )}
        {ssoError && step === 'credentials' ? (
          <p className={authStyles.error} role="alert">
            {ssoError}
          </p>
        ) : null}
        {errorMessage ? (
          <p className={authStyles.error} role="alert">
            {errorMessage}
          </p>
        ) : null}
        <div className={authStyles.actions}>
          {step === 'totp' ? (
            <StowButton variant="text" onClick={backToPassword}>
              {t('login.back')}
            </StowButton>
          ) : null}
          <StowButton
            type="submit"
            loading={pending}
            disabled={step === 'credentials' ? !username.trim() || !password : !code.trim()}
          >
            {step === 'credentials' ? t('login.sign') : t('common.ok')}
          </StowButton>
        </div>
        {step === 'credentials' && ssoName !== null ? (
          <>
            <div className={authStyles.divider}>
              <span>{t('login.or')}</span>
            </div>
            <StowButton variant="tonal" onClick={() => startOidcLogin(returnTo)}>
              {ssoName ? t('login.sign_with', { provider: ssoName }) : t('login.sign_with_single_sign')}
            </StowButton>
          </>
        ) : null}
        {step === 'credentials' ? (
          <Link className={cx(authStyles.setupLink, focusRing)} to="/setup">
            {t('login.first_time_here_create_administrator')}
          </Link>
        ) : null}
        <p className={authStyles.licence}>
          <a className={authStyles.licenceLink} href={SOURCE_URL} target="_blank" rel="noreferrer">
            {t('login.source')}
          </a>
        </p>
      </form>
    </main>
  )
}
