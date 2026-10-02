import '@mantine/core/styles/Badge.layer.css'
import { Badge } from '@mantine/core'
import type { ReactNode } from 'react'
import { cx } from '../cx'
import * as styles from './Badge.css'

export interface StowBadgeProps {
  /** Color only reinforces the text, which has to carry the meaning on its own. */
  tone?: keyof typeof styles.tone
  children: ReactNode
}

/** A short read-only label, such as a permission or a status. */
export function StowBadge({ tone = 'neutral', children }: StowBadgeProps) {
  // No color or variant prop, so Mantine leaves the colors to the tone class.
  return (
    <Badge className={cx(styles.root, styles.tone[tone])} classNames={{ label: styles.label }}>
      {children}
    </Badge>
  )
}
