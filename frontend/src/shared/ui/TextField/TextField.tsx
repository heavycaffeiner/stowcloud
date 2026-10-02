import '@mantine/core/styles/Input.layer.css'
import { TextInput } from '@mantine/core'
import type { ComponentPropsWithRef } from 'react'
import { type FieldPath, type FieldValues, useController, type UseControllerProps } from 'react-hook-form'
import { cx } from '../cx'
import * as field from '../field.css'

export type StowTextFieldType = 'text' | 'search' | 'password' | 'date' | 'datetime-local' | 'number'

export interface StowTextFieldProps extends Omit<
  ComponentPropsWithRef<'input'>,
  'value' | 'defaultValue' | 'onChange' | 'type' | 'size'
> {
  value?: string
  label?: string
  /** Hint under the field. The error replaces it while there is one. */
  helper?: string
  type?: StowTextFieldType
  error?: string | null
  onValueChange?: (value: string) => void
}

export function StowTextField({
  value = '',
  label,
  helper,
  type = 'text',
  error,
  autoFocus,
  className,
  onValueChange,
  ...rest
}: StowTextFieldProps) {
  return (
    <TextInput
      {...rest}
      type={type}
      value={value}
      label={label}
      description={error ? undefined : helper}
      error={error || undefined}
      errorProps={{ role: 'alert' }}
      autoFocus={autoFocus}
      // Dialogs move focus to the first marked element once their transition starts.
      data-autofocus={autoFocus || undefined}
      onChange={(event) => onValueChange?.(event.currentTarget.value)}
      className={cx(field.root, className)}
      classNames={field.classNames}
    />
  )
}

type StowFormTextFieldProps<T extends FieldValues, N extends FieldPath<T>> = Pick<
  UseControllerProps<T, N>,
  'control' | 'name' | 'rules'
> &
  Omit<StowTextFieldProps, 'value' | 'onValueChange' | 'name' | 'ref'>

/** A text field bound to one field of a react-hook-form form. A broken rule shows as the field's error. */
export function StowFormTextField<T extends FieldValues, N extends FieldPath<T>>({
  control,
  name,
  rules,
  error,
  ...props
}: StowFormTextFieldProps<T, N>) {
  const { field, fieldState } = useController({ control, name, rules })
  return (
    <StowTextField
      {...props}
      ref={field.ref}
      name={field.name}
      value={String(field.value ?? '')}
      onValueChange={field.onChange}
      onBlur={field.onBlur}
      error={fieldState.error?.message ?? error}
    />
  )
}
