import { Link } from 'react-router-dom'
import { Button } from '../../../lib/ui/Button'
import { TextField } from '../../../lib/ui/TextField'
import { useDocumentTitle } from '../../use-document-title'
import { useLoginFlow } from './use-login-flow'
import '../../../styles/app/routes/auth.css.ts'

const SOURCE_URL = 'https://github.com/heavycaffeiner/stowcloud'

export function LoginPage() {
  const { t, form, updateForm, returnTo, ssoName, ssoError, login, loginTotp, submitCredentials, submitTotp, startOidcLogin } = useLoginFlow()
  useDocumentTitle(t('login.sign_stowcloud'))
  const { step, factorMode, username, password, code, errorMessage } = form
  return <main className="sc-auth-page"><form className="sc-auth-card sc-auth-card--login" onSubmit={step === 'credentials' ? submitCredentials : submitTotp}>
    <h1 className="sc-auth-card-title">Stowcloud</h1><p className="sc-auth-card-subtitle">{returnTo ? t('login.sign_first_authorise_app') : t('login.sign_your_account')}</p>
    {step === 'credentials' ? <><TextField value={username} label={t('login.username')} autoFocus autoComplete="username" onValueChange={(username) => updateForm({ username })} /><TextField value={password} label={t('common.password')} type="password" autoComplete="current-password" onValueChange={(password) => updateForm({ password })} /></> : <><p className="sc-auth-card-subtitle">{factorMode === 'totp' ? t('login.enter_your_two_factor_code') : t('login.recovery_code_hint')}</p><TextField value={code} label={factorMode === 'totp' ? t('login.verification_code') : t('login.recovery_code')} placeholder={factorMode === 'totp' ? t('login.6_digits') : undefined} autoFocus autoComplete="one-time-code" onValueChange={(code) => updateForm({ code })} /><Button variant="text" onClick={() => updateForm((current) => ({ factorMode: current.factorMode === 'totp' ? 'recovery' : 'totp', code: '', errorMessage: null }))}>{factorMode === 'totp' ? t('login.use_recovery_code') : t('login.use_authenticator_code')}</Button></>}
    {ssoError && step === 'credentials' ? <p className="sc-auth-card-error" role="alert">{ssoError}</p> : null}{errorMessage ? <p className="sc-auth-card-error" role="alert">{errorMessage}</p> : null}
    <div className="sc-auth-card-actions">{step === 'totp' ? <Button variant="text" onClick={() => updateForm({ step: 'credentials', factorMode: 'totp', code: '', errorMessage: null })}>{t('login.back')}</Button> : null}<Button type="submit" loading={step === 'credentials' ? login.isPending : loginTotp.isPending} disabled={step === 'credentials' ? !username.trim() || !password : !code.trim()}>{step === 'credentials' ? t('login.sign') : t('common.ok')}</Button></div>
    {step === 'credentials' && ssoName !== null ? <><div className="sc-auth-card-divider"><span>{t('login.or')}</span></div><Button variant="tonal" onClick={() => startOidcLogin(returnTo)}>{ssoName ? t('login.sign_with', { provider: ssoName }) : t('login.sign_with_single_sign')}</Button></> : null}
    {step === 'credentials' ? <Link className="sc-auth-card-setup-link sc-focus-ring" to="/setup">{t('login.first_time_here_create_administrator')}</Link> : null}<p className="sc-auth-card-licence"><a href={SOURCE_URL} target="_blank" rel="noreferrer">{t('login.source')}</a></p>
  </form></main>
}
