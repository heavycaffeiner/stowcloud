import '@mantine/core/styles/Input.layer.css'
import { NativeSelect } from '@mantine/core'
import type { ComponentPropsWithRef } from 'react'
import { cx } from '../cx'
import * as field from '../field.css'

export interface StowSelectOption {
  value: string
  label: string
  disabled?: boolean
}

export interface StowSelectProps extends Omit<
  ComponentPropsWithRef<'select'>,
  'value' | 'defaultValue' | 'onChange' | 'children' | 'size'
> {
  value?: string
  label?: string
  options: StowSelectOption[]
  onChange?: (value: string) => void
}

/** A native select, so the platform picker handles keyboard, touch and screen readers. */
export function StowSelect({ value = '', label, options, className, onChange, ...rest }: StowSelectProps) {
  return (
    <NativeSelect
      {...rest}
      value={value}
      label={label}
      data={options}
      onChange={(event) => onChange?.(event.currentTarget.value)}
      className={cx(field.root, className)}
      classNames={field.classNames}
    />
  )
}
