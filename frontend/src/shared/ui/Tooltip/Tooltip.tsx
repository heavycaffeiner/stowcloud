import '@mantine/core/styles/Tooltip.layer.css'
import { Tooltip } from '@mantine/core'
import type { ReactElement } from 'react'
import * as styles from './Tooltip.css'

export interface StowTooltipProps {
  label: string
  disabled?: boolean
  /** One element that accepts a ref. */
  children: ReactElement
}

/** A short hint under a control, on hover or keyboard focus. Touch never shows it. */
export function StowTooltip({ label, disabled, children }: StowTooltipProps) {
  return (
    <Tooltip
      label={label}
      disabled={disabled}
      position="bottom"
      openDelay={400}
      events={{ hover: true, focus: true, touch: false }}
      classNames={{ tooltip: styles.tooltip }}
    >
      {children}
    </Tooltip>
  )
}
