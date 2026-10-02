import { useState } from 'react'
import { useForm } from 'react-hook-form'
import { useNavigate } from '@tanstack/react-router'
import { describeApiError } from '../../../api/error-text'
import { ApiError } from '../../../api/fetcher'
import { t } from '../../../i18n'
import { useCreateShare } from '../../admin/api'
import { type SetupFinding, SetupValidationError, useCreateInitialAdmin, useLoadSession, useLogin } from '../api'

export const MIN_PASSWORD_LENGTH = 10

export interface SetupValues {
  token: string
  username: string
  password: string
  passwordConfirm: string
  appHosts: string
  trustedProxies: string
  shareName: string
  sharePath: string
}

/** Where setup stands once the account exists. */
export interface SetupOutcome {
  warnings: SetupFinding[]
  shareFailed: boolean
  shareRetryError: string | null
  /** Signed in as the new administrator, so the folder picker can browse without the token. */
  signedIn: boolean
  loginFailed: boolean
}

export const toList = (value: string): string[] =>
  value
    .split(',')
    .map((item) => item.trim())
    .filter(Boolean)

/** Whether every step holds enough to create the account. A share needs both its name and its path, or neither. */
export function setupReady(values: SetupValues): boolean {
  return (
    values.token.trim().length > 0 &&
    values.username.trim().length > 0 &&
    values.password.length >= MIN_PASSWORD_LENGTH &&
    values.passwordConfirm === values.password &&
    toList(values.appHosts).length > 0 &&
    (values.shareName.trim() === '') === (values.sharePath.trim() === '')
  )
}

function messageFor(error: unknown): string {
  if (!(error instanceof ApiError)) return t('setup.could_not_create_administrator_account_2')
  if (error.code === 'setup.invalid_token') return t('setup.invalid_setup_token_check_server')
  if (error.code === 'setup.completed') return t('setup.setup_already_complete')
  if (error.code === 'setup.token_expired') return t('setup.setup_token_has_expired')
  return t('setup.could_not_create_administrator_account')
}

/** Creates the first administrator, signs in as it, and adds the first share when one was given. */
export function useSetup() {
  const navigate = useNavigate()
  const form = useForm<SetupValues>({
    defaultValues: {
      token: '',
      username: '',
      password: '',
      passwordConfirm: '',
      appHosts: location.hostname,
      trustedProxies: '',
      shareName: '',
      sharePath: ''
    }
  })
  // Findings that stopped the server from creating the account.
  const [findings, setFindings] = useState<SetupFinding[]>([])
  const [outcome, setOutcome] = useState<SetupOutcome | null>(null)
  const setup = useCreateInitialAdmin()
  const login = useLogin()
  const loadSession = useLoadSession()
  const retryShare = useCreateShare()
  const pending = setup.isPending || login.isPending || retryShare.isPending

  // A second factor cannot be asked for here; such an account signs in on the login page instead.
  async function signIn(username: string, password: string): Promise<boolean> {
    try {
      const result = await login.mutateAsync({ username: username.trim(), password })
      if (result.required === 'totp') return false
      await loadSession()
      return true
    } catch {
      return false
    }
  }

  async function continueAfterSetup(current: SetupOutcome): Promise<void> {
    const { username, password, shareName, sharePath } = form.getValues()
    let next: SetupOutcome = { ...current, shareRetryError: null }
    setOutcome(next)
    if (!next.signedIn) {
      if (!(await signIn(username, password))) {
        setOutcome({ ...next, loginFailed: true })
        return
      }
      next = { ...next, signedIn: true }
      setOutcome(next)
    }
    if (next.shareFailed) {
      try {
        await retryShare.mutateAsync({ name: shareName.trim(), host: sharePath.trim() })
        next = { ...next, shareFailed: false }
        setOutcome(next)
      } catch (error) {
        setOutcome({ ...next, shareRetryError: describeApiError(error, t('setup.first_share_retry_failed')) })
        return
      }
    }
    void navigate({ to: '/b/$', params: { _splat: '' }, replace: true })
  }

  const submit = form.handleSubmit(async (values) => {
    if (pending || !setupReady(values)) return
    setFindings([])
    try {
      const result = await setup.mutateAsync({
        token: values.token.trim(),
        username: values.username.trim(),
        password: values.password,
        app_hosts: toList(values.appHosts),
        trusted_proxies: toList(values.trustedProxies),
        first_share: values.shareName.trim()
          ? { name: values.shareName.trim(), host: values.sharePath.trim() }
          : undefined
      })
      const created: SetupOutcome = {
        warnings: result.warnings,
        shareFailed: result.share_failed === true,
        shareRetryError: null,
        signedIn: false,
        loginFailed: false
      }
      setOutcome(created)
      if (created.warnings.length === 0 && !created.shareFailed) await continueAfterSetup(created)
    } catch (error) {
      if (error instanceof SetupValidationError) setFindings(error.findings)
      else form.setError('root', { message: messageFor(error) })
    }
  })

  return {
    form,
    findings,
    outcome,
    submit,
    continueAfterSetup,
    creating: setup.isPending || login.isPending,
    signingIn: login.isPending,
    retrying: login.isPending || retryShare.isPending
  }
}
