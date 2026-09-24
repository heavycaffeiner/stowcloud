import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'
import { ApiError, api, isMock, type CreateShareReq } from '../../../lib/api/client'
import { describeApiError } from '../../../lib/api/error-text'
import { createInitialAdmin, SetupValidationError } from '../../../lib/api/setup'
import type { SetupActions, SetupState } from './use-setup-state'
import { keys } from '../../../lib/query/keys'
import { loginMutation } from '../../../lib/query/session'
import { useI18n } from '../../../lib/i18n/use-i18n'

export const MIN_PASSWORD_LENGTH = 10
export const toList = (value: string): string[] => value.split(',').map((item) => item.trim()).filter(Boolean)

export function useSetupFlows(state: SetupState, actions: SetupActions) {
  const { t } = useI18n()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const setup = useMutation({ mutationFn: createInitialAdmin })
  const login = useMutation(loginMutation())
  const retryShare = useMutation({ mutationFn: (req: CreateShareReq) => api.adminCreateShare(req) })
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
    actions.patch({ errorMessage: null, shareRetryError: null })
    if (!state.pickerAuthenticated) {
      try {
        const result = await login.mutateAsync({ username: state.username.trim(), password: state.password })
        if (result.required === 'totp') { actions.patch({ doneButLoginFailed: true }); return }
        await api.session()
        actions.patch({ pickerAuthenticated: true })
      } catch { actions.patch({ doneButLoginFailed: true }); return }
    }
    if (state.shareFailed) {
      try {
        await retryShare.mutateAsync({ name: state.shareName.trim(), host: state.sharePath.trim() })
        actions.patch({ shareFailed: false })
      } catch (error) {
        actions.patch({ shareRetryError: describeApiError(error, t('setup.first_share_retry_failed')) })
        return
      }
    }
    void navigate('/b/', { replace: true })
  }
  const submit = async (event: React.FormEvent): Promise<void> => {
    event.preventDefault()
    const canSubmit = !setup.isPending && !login.isPending && !retryShare.isPending && state.token.trim().length > 0 && state.username.trim().length > 0 && state.password.length >= MIN_PASSWORD_LENGTH && state.passwordConfirm === state.password && toList(state.appHosts).length > 0 && ((state.shareName.trim() === '') === (state.sharePath.trim() === ''))
    if (!canSubmit) return
    actions.patch({ errorMessage: null, shareRetryError: null, warnings: [], doneButLoginFailed: false })
    try {
      const result = await setup.mutateAsync({ token: state.token.trim(), username: state.username.trim(), password: state.password, app_hosts: toList(state.appHosts), trusted_proxies: toList(state.trustedProxies), first_share: state.shareName.trim() ? { name: state.shareName.trim(), host: state.sharePath.trim() } : undefined })
      actions.patch({ accountCreated: true, warnings: result.warnings, shareFailed: result.share_failed === true })
      void queryClient.invalidateQueries({ queryKey: keys.session() })
      void queryClient.invalidateQueries({ queryKey: keys.setupRequired() })
      if (result.warnings.length > 0 || result.share_failed === true) return
      await continueAfterSetup()
    } catch (error) {
      if (error instanceof SetupValidationError) { actions.patch({ warnings: error.findings }); return }
      actions.patch({ errorMessage: messageFor(error) })
    }
  }
  return { setup, login, retryShare, continueAfterSetup, submit, isMock }
}
