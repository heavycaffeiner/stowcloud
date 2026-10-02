import '@mantine/core/styles/Input.layer.css'
import { Textarea } from '@mantine/core'
import type { ComponentPropsWithRef } from 'react'
import { cx } from '../cx'
import * as field from '../field.css'
import * as styles from './TextArea.css'

export interface StowTextAreaProps extends Omit<
  ComponentPropsWithRef<'textarea'>,
  'value' | 'defaultValue' | 'onChange'
> {
  value?: string
  label?: string
  /** Hint under the field. */
  helper?: string
  /** Sets the text in the monospace face, for code and config documents. */
  monospace?: boolean
  onValueChange?: (value: string) => void
}

/** A multi-line text field that the user can resize vertically. */
export function StowTextArea({
  value = '',
  label,
  helper,
  monospace = false,
  className,
  onValueChange,
  ...rest
}: StowTextAreaProps) {
  return (
    <Textarea
      {...rest}
      value={value}
      label={label}
      description={helper}
      resize="vertical"
      onChange={(event) => onValueChange?.(event.currentTarget.value)}
      className={cx(field.root, styles.root, className)}
      classNames={{ ...field.classNames, input: cx(field.input, monospace && styles.monospace) }}
    />
  )
}
