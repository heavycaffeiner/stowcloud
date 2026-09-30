import type { ReactNode } from 'react'
import * as styles from './AdminCard.css'
import * as textFieldStyles from '../../ui/TextField.css'
import { cx } from '../../ui/cx'

interface AdminCardProps {
  readonly id: string
  readonly title: ReactNode
  readonly subtitle?: ReactNode
  readonly icon: ReactNode
  readonly headingLevel?: 'h3' | 'h4'
  readonly bodyClassName?: string
  readonly children: ReactNode
}

/** Shared accessible shell for administration settings cards. */
export function AdminCard({ id, title, subtitle, icon, headingLevel = 'h3', bodyClassName, children }: AdminCardProps) {
  const Heading = headingLevel
  const headingId = `${id}-title`
  return (
    <article className={cx(styles.root, textFieldStyles.onLowSurface)} id={id} aria-labelledby={headingId}>
      <div className={styles.head}>
        <div className={styles.icon}>{icon}</div>
        <div className={styles.meta}>
          <Heading className={styles.title} id={headingId}>
            {title}
          </Heading>
          {subtitle ? <p className={styles.subtitle}>{subtitle}</p> : null}
        </div>
      </div>
      {bodyClassName ? <div className={bodyClassName}>{children}</div> : children}
    </article>
  )
}
