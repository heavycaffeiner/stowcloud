import { useForm, useWatch } from 'react-hook-form'
import { describeApiError } from '../../../api/error-text'
import { PasswordStrengthMeter } from '../../auth/components/PasswordStrengthMeter'
import { validatePasswordChange } from '../../../lib/format/password-change'
import { useChangePassword } from '../api'
import { useI18n } from '../../../hooks/use-i18n'
import { StowButton, StowFormTextField } from '@/shared/ui'
import * as styles from './PasswordSection.css'
import * as settingsCardStyles from './SettingsCard.css'
import { ApiError } from '../../../api/fetcher'

interface PasswordValues {
  current: string
  next: string
  confirm: string
}

export function PasswordSection() {
  const { t } = useI18n()
  const save = useChangePassword()
  const { control, handleSubmit, setError, reset, formState } = useForm<PasswordValues>({
    defaultValues: { current: '', next: '', confirm: '' }
  })
  const [current, next] = useWatch({ control, name: ['current', 'next'] })

  const checkNext = (value: string, values: PasswordValues): string | true => {
    const problem = validatePasswordChange(value, values.confirm, 10)
    if (!problem) return true
    return problem.kind === 'too_short'
      ? t('password.must_at_least_characters', { min: problem.min })
      : t('password.new_passwords_do_not_match')
  }

  // A failed check hides the success of an earlier change.
  const submit = handleSubmit(
    async (values) => {
      if (save.isPending) return
      try {
        await save.mutateAsync({ current: values.current, next: values.next })
        reset()
      } catch (error) {
        if (error instanceof ApiError && error.code === 'auth.invalid_credentials')
          setError('current', { message: t('password.current_password_incorrect') })
        else if (error instanceof ApiError && error.code === 'auth.weak_password')
          setError('next', {
            message: t('password.must_at_least_characters', { min: error.reasonNumber('min_length') ?? 10 })
          })
        else setError('root', { message: describeApiError(error, t('password.could_not_change_password_try')) })
      }
    },
    () => save.reset()
  )

  return (
    <form className={styles.root} onSubmit={(event) => void submit(event)}>
      <StowFormTextField
        control={control}
        name="current"
        type="password"
        label={t('common.current_password')}
        autoComplete="current-password"
      />
      <StowFormTextField
        control={control}
        name="next"
        rules={{ validate: checkNext }}
        type="password"
        label={t('password.new_password')}
        autoComplete="new-password"
      />
      <PasswordStrengthMeter password={next} label={(level) => t('password.new_password_strength', { level })} />
      <StowFormTextField
        control={control}
        name="confirm"
        rules={{ deps: 'next' }}
        type="password"
        label={t('password.confirm_new_password')}
        autoComplete="new-password"
      />
      {formState.errors.root?.message ? (
        <p className={settingsCardStyles.error} role="alert">
          {formState.errors.root.message}
        </p>
      ) : null}
      {save.isSuccess ? (
        <p className={styles.success} role="status">
          {t('password.password_changed')}
        </p>
      ) : null}
      <div className={styles.actions}>
        <StowButton type="submit" disabled={!current || !next} loading={save.isPending}>
          {t('password.change_password')}
        </StowButton>
      </div>
    </form>
  )
}
