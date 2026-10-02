import { overlay } from 'overlay-kit'
import { useForm, useWatch } from 'react-hook-form'
import { describeApiError } from '../../../api/error-text'
import { scorePasswordStrength } from '../../auth/password-strength'
import { t } from '../../../i18n'
import { useI18n } from '../../../hooks/use-i18n'
import { StowButton, StowDialog, StowFormTextField, StowProgressLinear } from '@/shared/ui'
import * as adminStyles from './admin.css'
import { ApiError } from '../../../api/fetcher'
import { useCreateUser, useSetUserPassword, type AdminUser } from '../api'

const MIN_PASSWORD_LEN = 10

const longEnough = (password: string): string | true =>
  password.length >= MIN_PASSWORD_LEN || t('user.password_must_at_least_characters', { min: MIN_PASSWORD_LEN })

function StrengthMeter({ password, label }: { password: string; label: (level: string) => string }) {
  if (!password) return null
  const strength = scorePasswordStrength(password)
  return (
    <div>
      <StowProgressLinear value={strength.ratio} tone={strength.tier} label={label(strength.label)} />
      <span className={adminStyles.sectionFieldHint}>{strength.label}</span>
    </div>
  )
}

interface DialogControls<T> {
  open: boolean
  onDone: (result: T) => void
  onClosed: () => void
}

/** Creates an account. Resolves to the new user, or null when cancelled. */
export function askNewUser(): Promise<AdminUser | null> {
  return overlay.openAsync<AdminUser | null>(({ isOpen, close, unmount }) => (
    <NewUserDialog open={isOpen} onDone={close} onClosed={unmount} />
  ))
}

function NewUserDialog({ open, onDone, onClosed }: DialogControls<AdminUser | null>) {
  const { t } = useI18n()
  const create = useCreateUser()
  const { control, handleSubmit } = useForm({ defaultValues: { name: '', password: '' } })
  const password = useWatch({ control, name: 'password' })
  const submit = handleSubmit((values) => {
    if (values.name.trim() && !create.isPending)
      create.mutate({ name: values.name.trim(), password: values.password }, { onSuccess: onDone })
  })
  const cancel = (): void => {
    if (!create.isPending) onDone(null)
  }
  return (
    <StowDialog
      open={open}
      title={t('user.add_user')}
      onClose={cancel}
      onClosed={onClosed}
      actions={
        <>
          <StowButton variant="text" disabled={create.isPending} onClick={cancel}>
            {t('common.cancel')}
          </StowButton>
          <StowButton loading={create.isPending} onClick={() => void submit()}>
            {t('common.add')}
          </StowButton>
        </>
      }
    >
      <form className={adminStyles.form} onSubmit={(event) => void submit(event)}>
        <StowFormTextField control={control} name="name" label={t('user.username')} autoComplete="off" autoFocus />
        <StowFormTextField
          control={control}
          name="password"
          rules={{ validate: longEnough }}
          type="password"
          label={t('common.password')}
          autoComplete="new-password"
        />
        <StrengthMeter password={password} label={(level) => t('common.password_strength', { level })} />
        <p className={adminStyles.sectionFieldHint}>
          {t('user.at_least_characters_turning_smb', { min: MIN_PASSWORD_LEN })}
        </p>
        {create.error ? (
          <p className={adminStyles.sectionError} role="alert">
            {createErrorText(create.error)}
          </p>
        ) : null}
      </form>
    </StowDialog>
  )
}

function createErrorText(error: unknown): string {
  if (error instanceof ApiError && error.code === 'fs.conflict') return t('common.name_already_taken')
  if (error instanceof ApiError && error.code === 'auth.weak_password')
    return t('user.password_must_at_least_characters', { min: error.reasonNumber('min_length') ?? MIN_PASSWORD_LEN })
  return describeApiError(error, t('user.could_not_create_user'))
}

/** Sets a user's password. Resolves true once saved. */
export function askUserPassword(user: AdminUser): Promise<boolean> {
  return overlay.openAsync<boolean>(({ isOpen, close, unmount }) => (
    <UserPasswordDialog user={user} open={isOpen} onDone={close} onClosed={unmount} />
  ))
}

function UserPasswordDialog({ user, open, onDone, onClosed }: DialogControls<boolean> & { user: AdminUser }) {
  const { t } = useI18n()
  const save = useSetUserPassword()
  const { control, handleSubmit } = useForm({ defaultValues: { password: '', confirm: '' } })
  const [password, confirm] = useWatch({ control, name: ['password', 'confirm'] })
  const submit = handleSubmit((values) => {
    if (!save.isPending) save.mutate({ id: user.id, password: values.password }, { onSuccess: () => onDone(true) })
  })
  const cancel = (): void => {
    if (!save.isPending) onDone(false)
  }
  return (
    <StowDialog
      open={open}
      title={t('password.change_password')}
      onClose={cancel}
      onClosed={onClosed}
      actions={
        <>
          <StowButton variant="text" disabled={save.isPending} onClick={cancel}>
            {t('common.cancel')}
          </StowButton>
          <StowButton
            loading={save.isPending}
            disabled={!password || password !== confirm}
            onClick={() => void submit()}
          >
            {t('common.save')}
          </StowButton>
        </>
      }
    >
      <form className={adminStyles.form} onSubmit={(event) => void submit(event)}>
        <StowFormTextField
          control={control}
          name="password"
          rules={{ validate: longEnough }}
          type="password"
          label={t('password.new_password')}
          autoComplete="new-password"
          autoFocus
        />
        <StowFormTextField
          control={control}
          name="confirm"
          rules={{ validate: (value, values) => value === values.password || t('password.new_passwords_do_not_match') }}
          type="password"
          label={t('password.confirm_new_password')}
          autoComplete="new-password"
        />
        <StrengthMeter password={password} label={(level) => t('password.new_password_strength', { level })} />
        <p className={adminStyles.sectionFieldHint}>
          {t('password.must_at_least_characters', { min: MIN_PASSWORD_LEN })}
        </p>
        {save.error ? (
          <p className={adminStyles.sectionError} role="alert">
            {describeApiError(save.error, t('password.could_not_change_password_try'))}
          </p>
        ) : null}
      </form>
    </StowDialog>
  )
}
