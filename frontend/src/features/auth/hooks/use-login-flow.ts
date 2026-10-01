import { useState } from 'react'
import { useForm } from 'react-hook-form'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { t } from '../../../lib/i18n'
import { startOidcLogin, useLogin, useLoginTotp, useOidcConfig } from '../api'
import { ApiError } from '../../../api/fetcher'
import { oidcErrorMessage } from '../oidc-error'

export type FactorMode = 'totp' | 'recovery'

function safeReturnTo(raw: string | null): string | null {
  return raw && raw.startsWith('/') && !raw.startsWith('//') && !raw.startsWith('/\\') ? raw : null
}

function errorText(error: unknown): string {
  if (!(error instanceof ApiError)) return t('login.could_not_sign_check_your')
  if (error.code === 'auth.invalid_credentials') return t('login.incorrect_username_or_password')
  if (error.code === 'rate.limited') return t('login.too_many_sign_attempts_try')
  return t('login.could_not_sign_try_again')
}

/** Signs in with a password, then with a second factor when the account asks for one. */
export function useLoginFlow() {
  const navigate = useNavigate()
  const [searchParams] = useSearchParams()
  const form = useForm({ defaultValues: { username: '', password: '', code: '' } })
  // Set once the password is accepted and the account wants a second factor.
  const [challenge, setChallenge] = useState<string | null>(null)
  const [factorMode, setFactorMode] = useState<FactorMode>('totp')
  const returnTo = safeReturnTo(searchParams.get('returnTo'))
  const oidcConfig = useOidcConfig()
  const login = useLogin()
  const loginTotp = useLoginTotp()

  const finishLogin = (): void => {
    if (returnTo) window.location.href = returnTo
    else void navigate('/b/', { replace: true })
  }

  const pending = login.isPending || loginTotp.isPending
  // Enter in a field submits even while the button is busy.
  const submit = form.handleSubmit(async ({ username, password, code }) => {
    if (pending) return
    try {
      if (challenge !== null) {
        await loginTotp.mutateAsync({ challenge, code: code.trim() })
      } else {
        const result = await login.mutateAsync({ username: username.trim(), password })
        if (result.required === 'totp') {
          form.setValue('code', '')
          setFactorMode('totp')
          setChallenge(result.challenge)
          return
        }
      }
      finishLogin()
    } catch (error) {
      form.setError('root', { message: errorText(error) })
    }
  })

  const switchFactor = (): void => {
    setFactorMode((mode) => (mode === 'totp' ? 'recovery' : 'totp'))
    form.setValue('code', '')
    form.clearErrors('root')
  }

  const backToPassword = (): void => {
    setChallenge(null)
    setFactorMode('totp')
    form.setValue('code', '')
    form.clearErrors('root')
  }

  return {
    form,
    step: challenge === null ? ('credentials' as const) : ('totp' as const),
    factorMode,
    pending,
    returnTo,
    ssoName: oidcConfig.data?.enabled ? oidcConfig.data.display_name : null,
    ssoError: oidcErrorMessage(searchParams.get('oidc_error')),
    submit,
    switchFactor,
    backToPassword,
    startOidcLogin
  }
}
