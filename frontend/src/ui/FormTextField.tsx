import { type FieldPath, type FieldValues, type UseControllerProps, useController } from 'react-hook-form'
import { TextField, type TextFieldProps } from './TextField'

type FormTextFieldProps<T extends FieldValues, N extends FieldPath<T>> = Pick<
  UseControllerProps<T, N>,
  'control' | 'name' | 'rules'
> &
  Omit<TextFieldProps, 'value' | 'onValueChange' | 'name' | 'ref'>

/** A TextField bound to one field of a react-hook-form form. A broken rule shows as the field's error. */
export function FormTextField<T extends FieldValues, N extends FieldPath<T>>({
  control,
  name,
  rules,
  error,
  ...props
}: FormTextFieldProps<T, N>) {
  const { field, fieldState } = useController({ control, name, rules })
  return (
    <TextField
      {...props}
      ref={field.ref}
      name={field.name}
      value={String(field.value ?? '')}
      onValueChange={field.onChange}
      error={fieldState.error?.message ?? error}
    />
  )
}
