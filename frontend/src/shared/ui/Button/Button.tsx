import '@mantine/core/styles/UnstyledButton.layer.css'
import '@mantine/core/styles/Loader.layer.css'
import '@mantine/core/styles/Button.layer.css'
import { Button } from '@mantine/core'
import type { ComponentPropsWithRef, ReactNode } from 'react'
import { cx } from '../cx'
import * as styles from './Button.css'

export interface StowButtonProps extends Omit<ComponentPropsWithRef<'button'>, 'color'> {
  variant?: 'filled' | 'tonal' | 'outlined' | 'text'
  loading?: boolean
  danger?: boolean
  /** Renders a toggle button. Leave undefined for a plain action. */
  pressed?: boolean
  icon?: ReactNode
  endIcon?: ReactNode
}

export function StowButton({
  variant = 'filled',
  type = 'button',
  loading = false,
  danger = false,
  pressed,
  icon,
  endIcon,
  className,
  ...rest
}: StowButtonProps) {
  return (
    <Button
      type={type}
      variant={variant}
      color={danger ? 'danger' : undefined}
      loading={loading}
      aria-pressed={pressed}
      aria-busy={loading || undefined}
      leftSection={icon}
      rightSection={endIcon}
      className={cx(styles.root, className)}
      {...rest}
    />
  )
}
