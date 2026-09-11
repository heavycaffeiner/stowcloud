import { useMutation } from '@tanstack/react-query'
import type { FormEvent } from 'react'
import { useState } from 'react'
import { ApiError } from '../../api/client'
import { describeApiError } from '../../api/error-text'
import { scorePasswordStrength } from '../../format/password-strength'
import { validatePasswordChange } from '../../format/password-change'
import { changePasswordMutation } from '../../query/account'
import { useI18n } from '../../i18n/use-i18n'
import { Button } from '../Button'
import { TextField } from '../TextField'

export function PasswordSection() {
  const { t } = useI18n()
  const save = useMutation(changePasswordMutation())
  const [currentPassword, setCurrentPassword] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [confirmPassword, setConfirmPassword] = useState('')
  const [currentError, setCurrentError] = useState<string | null>(null)
  const [newError, setNewError] = useState<string | null>(null)
  const [formError, setFormError] = useState<string | null>(null)
  const [success, setSuccess] = useState(false)
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
