import type { ReactNode } from 'react'
import { cx } from './cx'
import * as styles from './ListItem.css'

export interface ListItemProps {
  selected?: boolean
  onClick?: () => void
  leading?: ReactNode
  trailing?: ReactNode
  headline: ReactNode
  supporting?: ReactNode
  className?: string
}

export function ListItem({
  selected = false,
  onClick,
  leading,
  trailing,
  headline,
  supporting,
  className
}: ListItemProps) {
  return (
    <div
      className={cx(styles.root, selected && styles.selected, onClick && styles.clickable, className)}
      onClick={onClick ? () => onClick() : undefined}
      role={onClick ? 'button' : 'presentation'}
      tabIndex={onClick ? 0 : undefined}
      onKeyDown={
        onClick
          ? (event) => {
              if (event.key === 'Enter' || event.key === ' ') {
                event.preventDefault()
                onClick()
              }
            }
          : undefined
      }
    >
      {leading ? <span className={styles.leading}>{leading}</span> : null}
      <span className={styles.text}>
        <span className={styles.headline}>{headline}</span>
        {supporting ? <span className={styles.supporting}>{supporting}</span> : null}
      </span>
      {trailing ? <span className={styles.trailing}>{trailing}</span> : null}
    </div>
  )
}
