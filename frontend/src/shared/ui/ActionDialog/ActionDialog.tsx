import type { ReactNode } from 'react'
import { useMutation } from '@tanstack/react-query'
import { overlay } from 'overlay-kit'
import { useForm } from 'react-hook-form'
import { useI18n } from '../../../hooks/use-i18n'
import { StowButton } from '../Button'
import { StowDialog } from '../Dialog'
import { StowFormTextField } from '../TextField'
import * as styles from './ActionDialog.css'

interface ActionOptions<T> {
  title: string
  body: ReactNode
  action: string
  danger?: boolean
  run: () => Promise<T>
  describeError: (error: unknown) => string
}

/** Asks before running an action. A failure keeps the dialog open with the error. Null when cancelled. */
export function confirmAction<T>(options: ActionOptions<T>): Promise<T | null> {
  return overlay.openAsync<T | null>(({ isOpen, close, unmount }) => (
    <ActionDialog {...options} open={isOpen} onDone={close} onClosed={unmount} />
  ))
}

interface ActionDialogProps<T> extends ActionOptions<T> {
  open: boolean
  onDone: (result: T | null) => void
  onClosed: () => void
}

function ActionDialog<T>({
  title,
  body,
  action,
  danger = false,
  run,
  describeError,
  open,
  onDone,
  onClosed
}: ActionDialogProps<T>) {
  const { t } = useI18n()
  const task = useMutation({ mutationFn: run })
  const cancel = (): void => {
    if (!task.isPending) onDone(null)
  }
  return (
    <StowDialog
      open={open}
      title={title}
      onClose={cancel}
      onClosed={onClosed}
      actions={
        <>
          <StowButton variant="text" disabled={task.isPending} onClick={cancel}>
            {t('common.cancel')}
          </StowButton>
          <StowButton
            danger={danger}
            loading={task.isPending}
            onClick={() => task.mutate(undefined, { onSuccess: onDone })}
          >
            {action}
          </StowButton>
        </>
      }
    >
      <div className={styles.body}>
        {body}
        {task.error ? (
          <p className={styles.error} role="alert">
            {describeError(task.error)}
          </p>
        ) : null}
      </div>
    </StowDialog>
  )
}

interface PromptOptions<T> {
  title: string
  label: string
  action: string
  initial?: string
  placeholder?: string
  hint?: ReactNode
  /** A message when the trimmed value cannot be submitted. */
  validate?: (value: string) => string | true
  run: (value: string) => Promise<T>
  describeError: (error: unknown) => string
}

/** Asks for one line of text and runs the action with it, trimmed. Null when cancelled. */
export function promptText<T>(options: PromptOptions<T>): Promise<T | null> {
  return overlay.openAsync<T | null>(({ isOpen, close, unmount }) => (
    <PromptDialog {...options} open={isOpen} onDone={close} onClosed={unmount} />
  ))
}

interface PromptDialogProps<T> extends PromptOptions<T> {
  open: boolean
  onDone: (result: T | null) => void
  onClosed: () => void
}

function PromptDialog<T>({
  title,
  label,
  action,
  initial = '',
  placeholder,
  hint,
  validate,
  run,
  describeError,
  open,
  onDone,
  onClosed
}: PromptDialogProps<T>) {
  const { t } = useI18n()
  const task = useMutation({ mutationFn: run })
  const { control, handleSubmit } = useForm({ defaultValues: { value: initial } })
  // Enter in the field submits even while the action runs.
  const submit = handleSubmit(({ value }) => {
    if (!task.isPending) task.mutate(value.trim(), { onSuccess: onDone })
  })
  const cancel = (): void => {
    if (!task.isPending) onDone(null)
  }
  return (
    <StowDialog
      open={open}
      title={title}
      onClose={cancel}
      onClosed={onClosed}
      actions={
        <>
          <StowButton variant="text" disabled={task.isPending} onClick={cancel}>
            {t('common.cancel')}
          </StowButton>
          <StowButton loading={task.isPending} onClick={() => void submit()}>
            {action}
          </StowButton>
        </>
      }
    >
      <form className={styles.body} onSubmit={(event) => void submit(event)}>
        <StowFormTextField
          control={control}
          name="value"
          rules={validate ? { validate: (value) => validate(value.trim()) } : undefined}
          label={label}
          placeholder={placeholder}
          autoComplete="off"
          autoFocus
        />
        {hint}
        {task.error ? (
          <p className={styles.error} role="alert">
            {describeError(task.error)}
          </p>
        ) : null}
      </form>
    </StowDialog>
  )
}
