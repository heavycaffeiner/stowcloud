import type { ReactNode } from 'react'
import * as styles from './SettingsCard.css'

interface SettingsCardProps {
  title: ReactNode
  description?: ReactNode
  leading?: ReactNode
  trailing?: ReactNode
  children: ReactNode
}

export function SettingsCard({ title, description, leading, trailing, children }: SettingsCardProps) {
  return (
    <article className={styles.card}>
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
