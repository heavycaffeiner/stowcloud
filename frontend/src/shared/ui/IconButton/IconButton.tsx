import '@mantine/core/styles/UnstyledButton.layer.css'
import '@mantine/core/styles/ActionIcon.layer.css'
import { ActionIcon } from '@mantine/core'
import type { ComponentPropsWithRef, ReactNode } from 'react'
import { cx } from '../cx'
import { Icon, type IconName } from '../Icon'
import { StowTooltip } from '../Tooltip'
import * as styles from './IconButton.css'

export interface StowIconButtonProps extends Omit<ComponentPropsWithRef<'button'>, 'color' | 'aria-label'> {
  /** The accessible name, also shown as the tooltip. */
  label: string
  icon?: IconName
  selectedIcon?: IconName
  /** Makes the button a toggle and fills it while on. */
  selected?: boolean
  /** Set when the button opens a menu or panel. A disclosure is never also a toggle. */
  expanded?: boolean
  /** Custom content, used when there is no `icon`. */
  children?: ReactNode
}

export function StowIconButton({
  label,
  icon,
  selectedIcon,
  selected,
  expanded,
  type = 'button',
  disabled,
  className,
  children,
  ...rest
}: StowIconButtonProps) {
  const current = selected && selectedIcon ? selectedIcon : icon
  return (
    <StowTooltip label={label} disabled={disabled}>
      <ActionIcon
        type={type}
        variant={selected ? 'tonal' : 'standard'}
        disabled={disabled}
        aria-label={label}
        aria-expanded={expanded}
        aria-pressed={expanded === undefined ? selected : undefined}
        className={cx(styles.root, className)}
        {...rest}
      >
        {current ? <Icon name={current} /> : children}
      </ActionIcon>
    </StowTooltip>
  )
}
