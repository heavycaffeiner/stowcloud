import '@mantine/core/styles/FloatingIndicator.layer.css'
import '@mantine/core/styles/SegmentedControl.layer.css'
import { SegmentedControl } from '@mantine/core'
import type { ReactNode } from 'react'
import { cx } from '../cx'
import * as styles from './SegmentedControl.css'

export interface StowSegmentedOption {
  value: string
  label: ReactNode
}

export interface StowSegmentedControlProps {
  /** The group's accessible name. */
  label: string
  value: string
  options: StowSegmentedOption[]
  onChange: (value: string) => void
  className?: string
}

/** A single choice among a few options. It always shows the owner's value, so a refused change does not stick. */
export function StowSegmentedControl({ label, value, options, onChange, className }: StowSegmentedControlProps) {
  return (
    <SegmentedControl
      aria-label={label}
      value={value}
      data={options}
      onChange={onChange}
      withItemsBorders={false}
      className={cx(styles.root, className)}
      classNames={{ indicator: styles.indicator, input: styles.input, label: styles.label }}
    />
  )
}
