import type { ReactNode } from 'react'
import { cx } from '../cx'
import * as styles from './ListItem.css'

export interface StowListItemProps {
  leading?: ReactNode
  headline: ReactNode
  supporting?: ReactNode
  trailing?: ReactNode
  className?: string
}

/** One row of a settings or management list: an optional icon, text, and trailing controls that wrap below. */
export function StowListItem({ leading, headline, supporting, trailing, className }: StowListItemProps) {
  return (
    <div className={cx(styles.root, className)}>
      {leading ? <span className={styles.leading}>{leading}</span> : null}
      <span className={styles.text}>
        <span className={styles.headline}>{headline}</span>
        {supporting ? <span className={styles.supporting}>{supporting}</span> : null}
      </span>
      {trailing ? <span className={styles.trailing}>{trailing}</span> : null}
    </div>
  )
}
