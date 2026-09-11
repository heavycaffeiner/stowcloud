import { useMutation, useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router-dom'
import { ApiError } from '../../lib/api/client'
import { oidcErrorMessage, startOidcLogin } from '../../lib/api/oidc'
import { useI18n } from '../../lib/i18n/use-i18n'
import { loginMutation, loginTotpMutation, oidcConfigQuery } from '../../lib/query/session'
import { Button } from '../../lib/ui/Button'
import { TextField } from '../../lib/ui/TextField'
import { useDocumentTitle } from '../use-document-title'
import './auth.css'

const SOURCE_URL = 'https://github.com/heavycaffeiner/stowcloud'

function safeReturnTo(raw: string | null): string | null {
  if (!raw || !raw.startsWith('/') || raw.startsWith('//') || raw.startsWith('/\\')) return null
  return raw
}

export function LoginPage() {
  const { t } = useI18n()
  const navigate = useNavigate()
  const [searchParams] = useSearchParams()
  const [step, setStep] = useState<'credentials' | 'totp'>('credentials')
  const [factorMode, setFactorMode] = useState<'totp' | 'recovery'>('totp')
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [code, setCode] = useState('')
  const [challenge, setChallenge] = useState('')
  const [errorMessage, setErrorMessage] = useState<string | null>(null)
  const returnTo = safeReturnTo(searchParams.get('returnTo'))
  const oidcConfig = useQuery(oidcConfigQuery())
  const login = useMutation(loginMutation())
  const loginTotp = useMutation(loginTotpMutation())
  const ssoName = oidcConfig.data?.enabled ? oidcConfig.data.display_name : null
  const ssoError = oidcErrorMessage(searchParams.get('oidc_error'))

  useDocumentTitle(t('login.sign_stowcloud'))

  const errorText = (error: unknown): string => {
    if (error instanceof ApiError) {
      if (error.code === 'auth.invalid_credentials') return t('login.incorrect_username_or_password')
      if (error.code === 'rate.limited') return t('login.too_many_sign_attempts_try')
      return t('login.could_not_sign_try_again')
    }
    return t('login.could_not_sign_check_your')
  }

  const finishLogin = (): void => {
    if (returnTo) {
      window.location.href = returnTo
      return
    }
    void navigate('/b/', { replace: true })
  }

  const submitCredentials = async (event: React.FormEvent): Promise<void> => {
    event.preventDefault()
    if (login.isPending || !username.trim() || !password) return
    setErrorMessage(null)
    try {
      const result = await login.mutateAsync({ username: username.trim(), password })
      if (result.required === 'totp') {
        setChallenge(result.challenge)
        setFactorMode('totp')
        setCode('')
        setStep('totp')
        return
      }
      finishLogin()
    } catch (error) {
      setErrorMessage(errorText(error))
    }
  }

  const submitTotp = async (event: React.FormEvent): Promise<void> => {
    event.preventDefault()
    if (loginTotp.isPending || code.trim().length === 0) return
    setErrorMessage(null)
    try {
      await loginTotp.mutateAsync({ challenge, code: code.trim() })
      finishLogin()
    } catch (error) {
      setErrorMessage(errorText(error))
    }
  }

  return (
    <main className="sc-auth-page">
      <form className="sc-auth-card" onSubmit={step === 'credentials' ? submitCredentials : submitTotp}>
        <h1 className="sc-auth-card__title">Stowcloud</h1>
        <p className="sc-auth-card__subtitle">
          {returnTo ? t('login.sign_first_authorise_app') : t('login.sign_your_account')}
        </p>

        {step === 'credentials' ? (
          <>
            <TextField
              value={username}
              label={t('login.username')}
              autoFocus
              autoComplete="username"
              onValueChange={setUsername}
            />
            <TextField
              value={password}
              label={t('common.password')}
              type="password"
              autoComplete="current-password"
              onValueChange={setPassword}
            />
          </>
        ) : (
          <>
            <p className="sc-auth-card__subtitle">
              {factorMode === 'totp'
                ? t('login.enter_your_two_factor_code')
                : t('login.recovery_code_hint')}
            </p>
            <TextField
              value={code}
              label={factorMode === 'totp' ? t('login.verification_code') : t('login.recovery_code')}
              placeholder={factorMode === 'totp' ? t('login.6_digits') : undefined}
              autoFocus
              autoComplete="one-time-code"
              onValueChange={setCode}
            />
            <Button
              variant="text"
              onClick={() => {
                setFactorMode(factorMode === 'totp' ? 'recovery' : 'totp')
                setCode('')
                setErrorMessage(null)
              }}
            >
              {factorMode === 'totp' ? t('login.use_recovery_code') : t('login.use_authenticator_code')}
            </Button>
          </>
        )}

        {ssoError && step === 'credentials' ? <p className="sc-auth-card__error" role="alert">{ssoError}</p> : null}
        {errorMessage ? <p className="sc-auth-card__error" role="alert">{errorMessage}</p> : null}

        <div className="sc-auth-card__actions">
          {step === 'totp' ? (
            <Button
              variant="text"
              onClick={() => {
                setStep('credentials')
                setFactorMode('totp')
                setCode('')
                setErrorMessage(null)
              }}
            >
              {t('login.back')}
            </Button>
          ) : null}
          <Button
            type="submit"
            loading={step === 'credentials' ? login.isPending : loginTotp.isPending}
            disabled={step === 'credentials' ? !username.trim() || !password : !code.trim()}
          >
            {step === 'credentials' ? t('login.sign') : t('common.ok')}
          </Button>
        </div>

        {step === 'credentials' && ssoName !== null ? (
          <>
            <div className="sc-auth-card__divider"><span>{t('login.or')}</span></div>
            <Button variant="tonal" onClick={() => startOidcLogin(returnTo)}>
              {ssoName ? t('login.sign_with', { provider: ssoName }) : t('login.sign_with_single_sign')}
            </Button>
          </>
        ) : null}

        {step === 'credentials' ? (
          <Link className="sc-auth-card__setup-link sc-focus-ring" to="/setup">
            {t('login.first_time_here_create_administrator')}
          </Link>
        ) : null}

        <p className="sc-auth-card__licence">
          <a href={SOURCE_URL} target="_blank" rel="noreferrer">{t('login.source')}</a>
        </p>
      </form>
    </main>
  )
}
