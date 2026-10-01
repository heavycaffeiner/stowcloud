import { Link } from 'react-router-dom'
import { Button } from '../../../ui/Button'
import { TextField } from '../../../ui/TextField'
import { useDocumentTitle } from '../../../hooks/use-document-title'
import { useLoginFlow } from '../hooks/use-login-flow'
import * as authStyles from './auth.css'
import * as utilitiesStyles from '../../../ui/utilities.css'
import { cx } from '../../../ui/cx'

const SOURCE_URL = 'https://github.com/heavycaffeiner/stowcloud'

export function LoginPage() {
  const {
    t,
    form,
    updateForm,
    returnTo,
    ssoName,
    ssoError,
    login,
    loginTotp,
    submitCredentials,
    submitTotp,
    startOidcLogin
  } = useLoginFlow()
  useDocumentTitle(t('login.sign_stowcloud'))
  const { step, factorMode, username, password, code, errorMessage } = form
  return (
    <main className={authStyles.page}>
      <form
        className={cx(authStyles.card, authStyles.login)}
        onSubmit={step === 'credentials' ? submitCredentials : submitTotp}
      >
        <h1 className={authStyles.title}>Stowcloud</h1>
        <p className={authStyles.subtitle}>
          {returnTo ? t('login.sign_first_authorise_app') : t('login.sign_your_account')}
        </p>
        {step === 'credentials' ? (
          <>
            <TextField
              value={username}
              label={t('login.username')}
              autoFocus
              autoComplete="username"
              onValueChange={(username) => updateForm({ username })}
            />
            <TextField
              value={password}
              label={t('common.password')}
              type="password"
              autoComplete="current-password"
              onValueChange={(password) => updateForm({ password })}
            />
          </>
        ) : (
          <>
            <p className={authStyles.subtitle}>
              {factorMode === 'totp' ? t('login.enter_your_two_factor_code') : t('login.recovery_code_hint')}
            </p>
            <TextField
              value={code}
              label={factorMode === 'totp' ? t('login.verification_code') : t('login.recovery_code')}
              placeholder={factorMode === 'totp' ? t('login.6_digits') : undefined}
              autoFocus
              autoComplete="one-time-code"
              onValueChange={(code) => updateForm({ code })}
            />
            <Button
              variant="text"
              onClick={() =>
                updateForm((current) => ({
                  factorMode: current.factorMode === 'totp' ? 'recovery' : 'totp',
                  code: '',
                  errorMessage: null
                }))
              }
            >
              {factorMode === 'totp' ? t('login.use_recovery_code') : t('login.use_authenticator_code')}
            </Button>
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
            <Button
              className={authStyles.action}
              variant="text"
              onClick={() => updateForm({ step: 'credentials', factorMode: 'totp', code: '', errorMessage: null })}
            >
              {t('login.back')}
            </Button>
          ) : null}
          <Button
            className={authStyles.action}
            type="submit"
            loading={step === 'credentials' ? login.isPending : loginTotp.isPending}
            disabled={step === 'credentials' ? !username.trim() || !password : !code.trim()}
          >
            {step === 'credentials' ? t('login.sign') : t('common.ok')}
          </Button>
        </div>
        {step === 'credentials' && ssoName !== null ? (
          <>
            <div className={authStyles.divider}>
              <span>{t('login.or')}</span>
            </div>
            <Button variant="tonal" onClick={() => startOidcLogin(returnTo)}>
              {ssoName ? t('login.sign_with', { provider: ssoName }) : t('login.sign_with_single_sign')}
            </Button>
          </>
        ) : null}
        {step === 'credentials' ? (
          <Link className={cx(authStyles.setupLink, utilitiesStyles.focusRing)} to="/setup">
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
