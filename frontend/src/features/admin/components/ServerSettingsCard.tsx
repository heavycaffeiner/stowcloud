import type { FormEvent, ReactNode } from 'react'
import { AdminCard } from './AdminCard'
import { Icon } from '@/shared/ui'
import * as styles from './ServerSettingsCard.css'

export interface ServerSettingsCardProps {
  id: string
  title: ReactNode
  subtitle: ReactNode
  children: ReactNode
  /** Makes the card body a form. */
  onSubmit?: (event: FormEvent<HTMLFormElement>) => void
}

export function ServerSettingsCard({ id, title, subtitle, children, onSubmit }: ServerSettingsCardProps) {
  return (
    <AdminCard id={id} title={title} subtitle={subtitle} icon={<Icon name="settings" />} headingLevel="h4">
      {onSubmit ? (
        <form className={styles.form} onSubmit={onSubmit}>
          {children}
        </form>
      ) : (
        <div className={styles.form}>{children}</div>
      )}
    </AdminCard>
  )
}
