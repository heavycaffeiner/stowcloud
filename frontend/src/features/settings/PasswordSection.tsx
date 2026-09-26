import { useMutation } from '@tanstack/react-query'
import type { FormEvent } from 'react'
import { useComponentState } from '../../lib/store/use-component-state'
import { ApiError } from '../../lib/api/client'
import { describeApiError } from '../../lib/api/error-text'
import { scorePasswordStrength } from '../../lib/format/password-strength'
import { validatePasswordChange } from '../../lib/format/password-change'
import { changePasswordMutation } from '../../lib/query/account'
import { useI18n } from '../../lib/i18n/use-i18n'
import { Button } from '../../lib/ui/Button'
import { TextField } from '../../lib/ui/TextField'

export function PasswordSection() {
  const { t } = useI18n()
  const save = useMutation(changePasswordMutation())
  type PasswordState = { current: string; next: string; confirm: string; currentError: string | null; nextError: string | null; formError: string | null; success: boolean }
  const [state, setState] = useComponentState<PasswordState>({ current: '', next: '', confirm: '', currentError: null, nextError: null, formError: null, success: false })
  const { current: currentPassword, next: newPassword, confirm: confirmPassword, currentError, nextError: newError, formError, success } = state
  const patchState = (patch: Partial<PasswordState>): void => setState((current) => ({ ...current, ...patch }))
  const setCurrentPassword = (value: string): void => patchState({ current: value })
  const setNewPassword = (value: string): void => patchState({ next: value })
  const setConfirmPassword = (value: string): void => patchState({ confirm: value })
  const setCurrentError = (value: string | null): void => patchState({ currentError: value })
  const setNewError = (value: string | null): void => patchState({ nextError: value })
  const setFormError = (value: string | null): void => patchState({ formError: value })
  const setSuccess = (value: boolean): void => patchState({ success: value })
  const strength = scorePasswordStrength(newPassword)

  function reset(): void {
    setCurrentPassword('')
    setNewPassword('')
    setConfirmPassword('')
  }

  async function submit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault()
    setCurrentError(null)
    setNewError(null)
    setFormError(null)
    setSuccess(false)
    const problem = validatePasswordChange(newPassword, confirmPassword, 10)
    if (problem) {
      setNewError(problem.kind === 'too_short' ? t('password.must_at_least_characters', { min: problem.min }) : t('password.new_passwords_do_not_match'))
      return
    }
    try {
      await save.mutateAsync({ current: currentPassword, next: newPassword })
      setSuccess(true)
      reset()
    } catch (error) {
      if (error instanceof ApiError && error.code === 'auth.invalid_credentials') setCurrentError(t('password.current_password_incorrect'))
      else if (error instanceof ApiError && error.code === 'auth.weak_password') setNewError(t('password.must_at_least_characters', { min: error.reasonNumber('min_length') ?? 10 }))
      else setFormError(describeApiError(error, t('password.could_not_change_password_try')))
    }
  }

  return (
    <form className="sc-password-form" onSubmit={submit}>
      <TextField type="password" label={t('common.current_password')} value={currentPassword} error={currentError} autoComplete="current-password" onValueChange={setCurrentPassword} />
      <TextField type="password" label={t('password.new_password')} value={newPassword} error={newError} autoComplete="new-password" onValueChange={setNewPassword} />
      {newPassword ? (
        <div className="sc-password-form__strength">
          <progress max={1} value={strength.ratio} aria-label={t('password.new_password_strength', { level: strength.label })} />
          <span className="sc-password-form__strength-label">{strength.label}</span>
        </div>
      ) : null}
      <TextField type="password" label={t('password.confirm_new_password')} value={confirmPassword} autoComplete="new-password" onValueChange={setConfirmPassword} />
      {formError ? <p className="sc-password-form__error" role="alert">{formError}</p> : null}
      {success ? <p className="sc-password-form__success" role="status">{t('password.password_changed')}</p> : null}
      <div className="sc-password-form__actions">
        <Button type="submit" disabled={!currentPassword || !newPassword} loading={save.isPending}>{t('password.change_password')}</Button>
      </div>
    </form>
  )
}
