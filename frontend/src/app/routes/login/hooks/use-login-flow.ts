import { useMutation, useQuery } from '@tanstack/react-query'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { ApiError } from '../../../../lib/api/client'
import { oidcErrorMessage, startOidcLogin } from '../../../../lib/api/oidc'
import { useI18n } from '../../../../hooks/use-i18n'
import { loginMutation, loginTotpMutation, oidcConfigQuery } from '../../../../lib/query/session'
import { useRouteStore, type RoutePatch } from '../../../hooks/use-route-store'

type LoginFormState = { step: 'credentials' | 'totp'; factorMode: 'totp' | 'recovery'; username: string; password: string; code: string; challenge: string; errorMessage: string | null }
function safeReturnTo(raw: string | null): string | null { return raw && raw.startsWith('/') && !raw.startsWith('//') && !raw.startsWith('/\\') ? raw : null }
export function useLoginFlow() {
  const { t } = useI18n(); const navigate = useNavigate(); const [searchParams] = useSearchParams()
  const [form, setForm] = useRouteStore<LoginFormState>({ step: 'credentials', factorMode: 'totp', username: '', password: '', code: '', challenge: '', errorMessage: null })
  const updateForm = (patch: RoutePatch<LoginFormState>): void => setForm(patch)
  const returnTo = safeReturnTo(searchParams.get('returnTo')); const oidcConfig = useQuery(oidcConfigQuery()); const login = useMutation(loginMutation()); const loginTotp = useMutation(loginTotpMutation())
  const errorText = (error: unknown): string => error instanceof ApiError ? (error.code === 'auth.invalid_credentials' ? t('login.incorrect_username_or_password') : error.code === 'rate.limited' ? t('login.too_many_sign_attempts_try') : t('login.could_not_sign_try_again')) : t('login.could_not_sign_check_your')
  const finishLogin = (): void => { if (returnTo) { window.location.href = returnTo; return }; void navigate('/b/', { replace: true }) }
  const submitCredentials = async (event: React.FormEvent): Promise<void> => { event.preventDefault(); if (login.isPending || !form.username.trim() || !form.password) return; updateForm({ errorMessage: null }); try { const result = await login.mutateAsync({ username: form.username.trim(), password: form.password }); if (result.required === 'totp') { updateForm({ challenge: result.challenge, factorMode: 'totp', code: '', step: 'totp' }); return }; finishLogin() } catch (error) { updateForm({ errorMessage: errorText(error) }) } }
  const submitTotp = async (event: React.FormEvent): Promise<void> => { event.preventDefault(); if (loginTotp.isPending || !form.code.trim()) return; updateForm({ errorMessage: null }); try { await loginTotp.mutateAsync({ challenge: form.challenge, code: form.code.trim() }); finishLogin() } catch (error) { updateForm({ errorMessage: errorText(error) }) } }
  return { t, form, updateForm, returnTo, ssoName: oidcConfig.data?.enabled ? oidcConfig.data.display_name : null, ssoError: oidcErrorMessage(searchParams.get('oidc_error')), login, loginTotp, submitCredentials, submitTotp, startOidcLogin }
}
