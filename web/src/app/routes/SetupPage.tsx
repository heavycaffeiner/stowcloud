import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { ApiError, api, isMock, type CreateShareReq } from '../../lib/api/client'
import { describeApiError } from '../../lib/api/error-text'
import { createInitialAdmin, SetupValidationError, type SetupFinding } from '../../lib/api/setup'
import { scorePasswordStrength } from '../../lib/format/password-strength'
import { useI18n } from '../../lib/i18n/use-i18n'
import { keys } from '../../lib/query/keys'
import { loginMutation } from '../../lib/query/session'
import { Button } from '../../lib/ui/Button'
import { PathPickerDialog } from '../../lib/ui/PathPickerDialog'
import { TextField } from '../../lib/ui/TextField'
import { useDocumentTitle } from '../use-document-title'
import './auth.css'

const MIN_PASSWORD_LENGTH = 10

function toList(value: string): string[] {
  return value.split(',').map((item) => item.trim()).filter(Boolean)
}

function warningText(t: (key: string, params?: Record<string, string | number>) => string, finding: SetupFinding): string {
  return t(finding.reason, finding.args ?? {})
}

export function SetupPage() {
  const { t } = useI18n()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const [token, setToken] = useState('')
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [passwordConfirm, setPasswordConfirm] = useState('')
  const [appHosts, setAppHosts] = useState(() => (typeof location === 'undefined' ? '' : location.hostname))
  const [trustedProxies, setTrustedProxies] = useState('')
  const [shareName, setShareName] = useState('')
  const [sharePath, setSharePath] = useState('')
  const [accountCreated, setAccountCreated] = useState(false)
  const [doneButLoginFailed, setDoneButLoginFailed] = useState(false)
  const [shareFailed, setShareFailed] = useState(false)
  const [shareRetryError, setShareRetryError] = useState<string | null>(null)
  const [errorMessage, setErrorMessage] = useState<string | null>(null)
  const [warnings, setWarnings] = useState<SetupFinding[]>([])
  const [pickerOpen, setPickerOpen] = useState(false)
  const [pickerAuthenticated, setPickerAuthenticated] = useState(false)

  const setup = useMutation({ mutationFn: createInitialAdmin })
  const login = useMutation(loginMutation())
  const retryShare = useMutation({ mutationFn: (req: CreateShareReq) => api.adminCreateShare(req) })
  const strength = scorePasswordStrength(password)
  const passwordError = password && password.length < MIN_PASSWORD_LENGTH
    ? t('setup.password_must_at_least_characters', { min: MIN_PASSWORD_LENGTH })
    : null
  const confirmError = passwordConfirm && passwordConfirm !== password ? t('setup.passwords_do_not_match') : null
  const canSubmit = !setup.isPending && !login.isPending && !retryShare.isPending &&
    token.trim().length > 0 && username.trim().length > 0 &&
    password.length >= MIN_PASSWORD_LENGTH && passwordConfirm === password &&
    toList(appHosts).length > 0 && ((shareName.trim() === '') === (sharePath.trim() === ''))
  const canRetryShare = !login.isPending && !retryShare.isPending && shareName.trim().length > 0 && sharePath.trim().length > 0

  useDocumentTitle(t('setup.create_administrator_account'))

  const messageFor = (error: unknown): string => {
    if (error instanceof ApiError) {
      if (error.code === 'setup.invalid_token') return t('setup.invalid_setup_token_check_server')
      if (error.code === 'setup.completed') return t('setup.setup_already_complete')
      if (error.code === 'setup.token_expired') return t('setup.setup_token_has_expired')
      return t('setup.could_not_create_administrator_account')
    }
    return t('setup.could_not_create_administrator_account_2')
  }

  const continueAfterSetup = async (): Promise<void> => {
    setErrorMessage(null)
    setShareRetryError(null)
    if (!pickerAuthenticated) {
      try {
        const result = await login.mutateAsync({ username: username.trim(), password })
        if (result.required === 'totp') {
          setDoneButLoginFailed(true)
          return
        }
        await api.session()
        setPickerAuthenticated(true)
      } catch {
        setDoneButLoginFailed(true)
        return
      }
    }
    if (shareFailed) {
      try {
        await retryShare.mutateAsync({ name: shareName.trim(), host: sharePath.trim() })
        setShareFailed(false)
      } catch (error) {
        setShareRetryError(describeApiError(error, t('setup.first_share_retry_failed')))
        return
      }
    }
    void navigate('/b/', { replace: true })
  }

  const submit = async (event: React.FormEvent): Promise<void> => {
    event.preventDefault()
    if (!canSubmit) return
    setErrorMessage(null)
    setShareRetryError(null)
    setWarnings([])
    setDoneButLoginFailed(false)
    try {
      const result = await setup.mutateAsync({
        token: token.trim(),
        username: username.trim(),
        password,
        app_hosts: toList(appHosts),
        trusted_proxies: toList(trustedProxies),
        first_share: shareName.trim() ? { name: shareName.trim(), host: sharePath.trim() } : undefined
      })
      setAccountCreated(true)
      setWarnings(result.warnings)
      setShareFailed(result.share_failed === true)
      void queryClient.invalidateQueries({ queryKey: keys.session() })
      void queryClient.invalidateQueries({ queryKey: keys.setupRequired() })
      if (result.warnings.length > 0 || result.share_failed === true) return
      await continueAfterSetup()
    } catch (error) {
      if (error instanceof SetupValidationError) {
        setWarnings(error.findings)
        return
      }
      setErrorMessage(messageFor(error))
    }
  }

  return (
    <main className="sc-auth-page">
      <form className="sc-auth-card" onSubmit={submit}>
        <h1 className="sc-auth-card__title">{t('setup.create_administrator_account')}</h1>
        <p className="sc-auth-card__subtitle">
          {t('setup.on_server_s_first_start')} <code>setup-token</code> {t('setup.file_data_directory')}
          {isMock ? <><br /><em>{t('setup.mock_mode_any_token_value')}</em></> : null}
        </p>

        {accountCreated ? (
          <>
            <p className="sc-auth-card__success" role="status">{t('setup.account_created')}</p>
            {doneButLoginFailed ? (
              <p className="sc-auth-card__error" role="alert">
                <Link to="/login">{t('setup.go_sign_page')}</Link> {t('setup.sign')}
              </p>
            ) : null}
            {warnings.length > 0 ? (
              <div className="sc-auth-card__warning" role="status">
                {warnings.map((warning, index) => <p key={`${warning.reason}-${index}`}>{warningText(t, warning)}</p>)}
              </div>
            ) : null}
            {shareFailed ? (
              <>
                <p className="sc-auth-card__error" role="alert">{t('setup.account_created_share_failed')}</p>
                <TextField value={shareName} label={t('common.name')} autoComplete="off" onValueChange={setShareName} />
                <div className="sc-auth-card__path-row">
                  <TextField value={sharePath} label={t('folder_share.server_path')} autoComplete="off" onValueChange={setSharePath} />
                  <Button variant="outlined" disabled={!pickerAuthenticated} onClick={() => setPickerOpen(true)}>{t('picker.browse_folder')}</Button>
                </div>
                {shareRetryError ? <p className="sc-auth-card__error" role="alert">{shareRetryError}</p> : null}
                {shareRetryError && pickerAuthenticated ? <Link className="sc-auth-card__setup-link sc-focus-ring" to="/admin#shares">{t('setup.go_to_admin_shares')}</Link> : null}
                <div className="sc-auth-card__actions">
                  <Button disabled={!canRetryShare} loading={login.isPending || retryShare.isPending} onClick={() => void continueAfterSetup()}>{t('setup.retry_first_share')}</Button>
                </div>
              </>
            ) : (
              <div className="sc-auth-card__actions">
                <Button loading={login.isPending} onClick={() => void continueAfterSetup()}>{t('setup.continue_anyway')}</Button>
              </div>
            )}
          </>
        ) : (
          <>
            <TextField value={token} label={t('setup.setup_token')} autoFocus autoComplete="off" onValueChange={setToken} />
            <TextField value={username} label={t('setup.administrator_username')} autoComplete="username" onValueChange={setUsername} />
            <TextField value={password} label={t('common.password')} type="password" error={passwordError} autoComplete="new-password" onValueChange={setPassword} />
            {password ? (
              <div className="sc-auth-card__strength">
                <mdui-linear-progress value={strength.ratio} max={1} aria-label={t('common.password_strength', { level: strength.label })}></mdui-linear-progress>
                <span className="sc-auth-card__strength-label">{strength.label}</span>
              </div>
            ) : null}
            <TextField value={passwordConfirm} label={t('setup.confirm_password')} type="password" error={confirmError} autoComplete="new-password" onValueChange={setPasswordConfirm} />
            <h2 className="sc-auth-card__section">{t('setup.how_this_server_is_reached')}</h2>
            <TextField value={appHosts} label={t('server.app_hosts_comma_separated')} autoComplete="off" onValueChange={setAppHosts} />
            <p className="sc-auth-card__hint">{t('setup.app_hosts_hint')}</p>
            <TextField value={trustedProxies} label={t('server.trusted_proxies_comma_separated')} autoComplete="off" onValueChange={setTrustedProxies} />
            <p className="sc-auth-card__hint">{t('setup.trusted_proxies_hint')}</p>
            <h2 className="sc-auth-card__section">{t('setup.first_shared_folder')}</h2>
            <p className="sc-auth-card__hint">{t('setup.first_share_hint')}</p>
            <TextField value={shareName} label={t('common.name')} autoComplete="off" onValueChange={setShareName} />
            <div className="sc-auth-card__path-row">
              <TextField value={sharePath} label={t('folder_share.server_path')} autoComplete="off" onValueChange={setSharePath} />
              <Button variant="outlined" onClick={() => setPickerOpen(true)}>{t('picker.browse_folder')}</Button>
            </div>
            {warnings.length > 0 ? (
              <div className="sc-auth-card__warning" role="alert">
                {warnings.map((warning, index) => <p key={`${warning.reason}-${index}`}>{warningText(t, warning)}</p>)}
              </div>
            ) : null}
            {errorMessage ? <p className="sc-auth-card__error" role="alert">{errorMessage}</p> : null}
            <div className="sc-auth-card__actions">
              <Button type="submit" disabled={!canSubmit} loading={setup.isPending || login.isPending}>{t('setup.create_administrator_account')}</Button>
            </div>
            <Link className="sc-auth-card__setup-link sc-focus-ring" to="/login">{t('setup.already_have_account_sign')}</Link>
          </>
        )}
      </form>
      <PathPickerDialog
        open={pickerOpen}
        mode="folder"
        start={sharePath}
        token={pickerAuthenticated ? undefined : token}
        onclose={() => setPickerOpen(false)}
        onpick={(path) => { setSharePath(path); setPickerOpen(false) }}
      />
    </main>
  )
}
