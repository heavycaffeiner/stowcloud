import { Link } from 'react-router-dom'
import { scorePasswordStrength } from '../../../lib/format/password-strength'
import { useI18n } from '../../../lib/i18n/use-i18n'
import { Button } from '../../../lib/ui/Button'
import { PathPickerDialog } from '../../../features/files/PathPickerDialog'
import { TextField } from '../../../lib/ui/TextField'
import { useDocumentTitle } from '../../use-document-title'
import { useSetupFlows, MIN_PASSWORD_LENGTH, toList } from './use-setup-flows'
import { useSetupState } from './use-setup-state'
import type { SetupFinding } from '../../../lib/api/setup'
import '../../../styles/app/routes/auth.css.ts'

function warningText(t: (key: string, params?: Record<string, string | number>) => string, finding: SetupFinding): string { return t(finding.reason, finding.args ?? {}) }

export function SetupPage() {
  const { t } = useI18n()
  const { state, actions } = useSetupState()
  const flows = useSetupFlows(state, actions)
  const { token, username, password, passwordConfirm, appHosts, trustedProxies, shareName, sharePath, accountCreated, doneButLoginFailed, shareFailed, shareRetryError, errorMessage, warnings, pickerOpen, pickerAuthenticated, step } = state
  const strength = scorePasswordStrength(password)
  const passwordError = password && password.length < MIN_PASSWORD_LENGTH ? t('setup.password_must_at_least_characters', { min: MIN_PASSWORD_LENGTH }) : null
  const confirmError = passwordConfirm && passwordConfirm !== password ? t('setup.passwords_do_not_match') : null
  const canSubmit = !flows.setup.isPending && !flows.login.isPending && !flows.retryShare.isPending && token.trim().length > 0 && username.trim().length > 0 && password.length >= MIN_PASSWORD_LENGTH && passwordConfirm === password && toList(appHosts).length > 0 && ((shareName.trim() === '') === (sharePath.trim() === ''))
  useDocumentTitle(t('setup.create_administrator_account'))
  return (
    <main className="sc-auth-page">
      <form className="sc-auth-card" onSubmit={flows.submit}>
        <h1 className="sc-auth-card-title">{t('setup.create_administrator_account')}</h1>
        <p className="sc-auth-card-subtitle">{t('setup.on_server_s_first_start')} <code>setup-token</code> {t('setup.file_data_directory')}{flows.isMock ? <><br /><em>{t('setup.mock_mode_any_token_value')}</em></> : null}</p>
        {accountCreated ? <>
          <p className="sc-auth-card-success" role="status">{t('setup.account_created')}</p>
          {doneButLoginFailed ? <p className="sc-auth-card-error" role="alert"><Link to="/login">{t('setup.go_sign_page')}</Link> {t('setup.sign')}</p> : null}
          {warnings.length > 0 ? <div className="sc-auth-card-warning" role="status">{warnings.map((warning, index) => <p key={`${warning.reason}-${index}`}>{warningText(t, warning)}</p>)}</div> : null}
          {shareFailed ? <><p className="sc-auth-card-error" role="alert">{t('setup.account_created_share_failed')}</p><TextField value={shareName} label={t('common.name')} autoComplete="off" onValueChange={actions.setShareName} /><div className="sc-auth-card-path-row"><TextField value={sharePath} label={t('folder_share.server_path')} autoComplete="off" onValueChange={actions.setSharePath} /><Button variant="outlined" disabled={!pickerAuthenticated} onClick={() => actions.patch({ pickerOpen: true })}>{t('picker.browse_folder')}</Button></div>{shareRetryError ? <p className="sc-auth-card-error" role="alert">{shareRetryError}</p> : null}{shareRetryError && pickerAuthenticated ? <Link className="sc-auth-card-setup-link sc-focus-ring" to="/admin#shares">{t('setup.go_to_admin_shares')}</Link> : null}<div className="sc-auth-card-actions"><Button disabled={flows.login.isPending || flows.retryShare.isPending || shareName.trim().length === 0 || sharePath.trim().length === 0} loading={flows.login.isPending || flows.retryShare.isPending} onClick={() => void flows.continueAfterSetup()}>{t('setup.retry_first_share')}</Button></div></> : <div className="sc-auth-card-actions"><Button loading={flows.login.isPending} onClick={() => void flows.continueAfterSetup()}>{t('setup.continue_anyway')}</Button></div>}
        </> : <>
          <nav className="sc-auth-card-steps" aria-label={t('setup.create_administrator_account')}><span className={step === 1 ? 'is-active' : ''}>1. {t('setup.create_administrator_account')}</span><span className={step === 2 ? 'is-active' : ''}>2. {t('setup.how_this_server_is_reached')}</span><span className={step === 3 ? 'is-active' : ''}>3. {t('setup.first_shared_folder')}</span></nav>
          {step === 1 ? <><TextField value={token} label={t('setup.setup_token')} autoFocus autoComplete="off" onValueChange={actions.setToken} /><TextField value={username} label={t('setup.administrator_username')} autoComplete="username" onValueChange={actions.setUsername} /><TextField value={password} label={t('common.password')} type="password" error={passwordError} autoComplete="new-password" onValueChange={actions.setPassword} />{password ? <div className="sc-auth-card-strength"><mdui-linear-progress value={strength.ratio} max={1} aria-label={t('common.password_strength', { level: strength.label })}></mdui-linear-progress><span className="sc-auth-card-strength-label">{strength.label}</span></div> : null}<TextField value={passwordConfirm} label={t('setup.confirm_password')} type="password" error={confirmError} autoComplete="new-password" onValueChange={actions.setPasswordConfirm} /></> : null}
          {step === 2 ? <><TextField value={appHosts} label={t('server.app_hosts_comma_separated')} autoComplete="off" onValueChange={actions.setAppHosts} /><p className="sc-auth-card-hint">{t('setup.app_hosts_hint')}</p><TextField value={trustedProxies} label={t('server.trusted_proxies_comma_separated')} autoComplete="off" onValueChange={actions.setTrustedProxies} /><p className="sc-auth-card-hint">{t('setup.trusted_proxies_hint')}</p></> : null}
          {step === 3 ? <><p className="sc-auth-card-hint">{t('setup.first_share_hint')}</p><TextField value={shareName} label={t('common.name')} autoComplete="off" onValueChange={actions.setShareName} /><div className="sc-auth-card-path-row"><TextField value={sharePath} label={t('folder_share.server_path')} autoComplete="off" onValueChange={actions.setSharePath} /><Button variant="outlined" onClick={() => actions.patch({ pickerOpen: true })}>{t('picker.browse_folder')}</Button></div></> : null}
          {warnings.length > 0 ? <div className="sc-auth-card-warning" role="alert">{warnings.map((warning, index) => <p key={`${warning.reason}-${index}`}>{warningText(t, warning)}</p>)}</div> : null}{errorMessage ? <p className="sc-auth-card-error" role="alert">{errorMessage}</p> : null}
          <div className="sc-auth-card-actions">{step > 1 ? <Button variant="outlined" type="button" onClick={actions.previousStep}>{t('common.back')}</Button> : null}{step < 3 ? <Button type="button" onClick={actions.nextStep}>{t('common.continue')}</Button> : <Button type="submit" disabled={!canSubmit} loading={flows.setup.isPending || flows.login.isPending}>{t('setup.create_administrator_account')}</Button>}</div><Link className="sc-auth-card-setup-link sc-focus-ring" to="/login">{t('setup.already_have_account_sign')}</Link>
        </>}
      </form>
      <PathPickerDialog open={pickerOpen} mode="folder" start={sharePath} token={pickerAuthenticated ? undefined : token} onclose={() => actions.patch({ pickerOpen: false })} onpick={(path) => actions.patch({ sharePath: path, pickerOpen: false })} />
    </main>
  )
}
