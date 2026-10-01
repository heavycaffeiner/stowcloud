import type { ReactNode } from 'react'
import { useMutation } from '@tanstack/react-query'
import { overlay } from 'overlay-kit'
import { useForm, useWatch } from 'react-hook-form'
import { ApiError } from '../../api/fetcher'
import { useI18n } from '../../hooks/use-i18n'
import { Button } from '../../ui/Button'
import { FormTextField } from '../../ui/FormTextField'
import { SettingsDialog } from './SettingsDialog'

interface PasswordPromptOptions<T> {
  title: string
  body: ReactNode
  action: string
  run: (password: string) => Promise<T>
  /** Words any failure other than a wrong password. */
  describeError: (error: unknown) => string
}

/** Asks for the current password and runs the action with it. Null when cancelled. */
export function askPassword<T>(options: PasswordPromptOptions<T>): Promise<T | null> {
  return overlay.openAsync<T | null>(({ isOpen, close, unmount }) => (
    <PasswordPrompt {...options} open={isOpen} onDone={close} onClosed={unmount} />
  ))
}

interface PasswordPromptProps<T> extends PasswordPromptOptions<T> {
  open: boolean
  onDone: (result: T | null) => void
  onClosed: () => void
}

function PasswordPrompt<T>({
  title,
  body,
  action,
  run,
  describeError,
  open,
  onDone,
  onClosed
}: PasswordPromptProps<T>) {
  const { t } = useI18n()
  const task = useMutation({ mutationFn: run })
  const { control, handleSubmit } = useForm({ defaultValues: { password: '' } })
  const password = useWatch({ control, name: 'password' })
  // Enter in the field submits even while the action runs.
  const submit = handleSubmit((values) => {
    if (values.password && !task.isPending) task.mutate(values.password, { onSuccess: onDone })
  })
  const cancel = (): void => {
    if (!task.isPending) onDone(null)
  }
  const error = task.error
    ? task.error instanceof ApiError && task.error.code === 'auth.invalid_credentials'
      ? t('common.incorrect_password')
      : describeError(task.error)
    : null
  return (
    <SettingsDialog
      open={open}
      title={title}
      onClose={cancel}
      onClosed={onClosed}
      onSubmit={(event) => void submit(event)}
      actions={
        <>
          <Button variant="text" disabled={task.isPending} onClick={cancel}>
            {t('common.cancel')}
          </Button>
          <Button disabled={!password} loading={task.isPending} onClick={() => void submit()}>
            {action}
          </Button>
        </>
      }
    >
      {body}
      <FormTextField
        control={control}
        name="password"
        type="password"
        label={t('common.current_password')}
        autoComplete="current-password"
        error={error}
      />
    </SettingsDialog>
  )
}
