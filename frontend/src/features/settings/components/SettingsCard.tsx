import type { ReactNode } from 'react'
import * as styles from './SettingsCard.css'
import * as textFieldStyles from '../../../ui/TextField.css'
import { cx } from '../../../ui/cx'

interface SettingsCardProps {
  title: ReactNode
  description?: ReactNode
  leading?: ReactNode
  trailing?: ReactNode
  children: ReactNode
}

export function SettingsCard({ title, description, leading, trailing, children }: SettingsCardProps) {
  return (
    <article className={cx(styles.card, textFieldStyles.onLowSurface)}>
      <div className={styles.cardHead}>
        {leading}
        <div className={styles.cardMeta}>
          <h2 className={styles.cardTitle}>{title}</h2>
          {description}
        </div>
        {trailing}
      </div>
      {children}
    </article>
  )
}
