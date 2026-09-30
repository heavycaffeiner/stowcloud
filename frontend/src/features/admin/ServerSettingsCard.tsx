import type { ReactNode } from 'react'
import { AdminCard } from './AdminCard'
import { Icon } from '../../ui/Icon'
import * as styles from './ServerSettingsCard.css'

export interface ServerSettingsCardProps {
  id: string
  title: ReactNode
  subtitle: ReactNode
  children: ReactNode
}

export function ServerSettingsCard({ id, title, subtitle, children }: ServerSettingsCardProps) {
  return (
    <AdminCard
      id={id}
      title={title}
      subtitle={subtitle}
      icon={<Icon name="settings" />}
      headingLevel="h4"
      bodyClassName={styles.form}
    >
      {children}
    </AdminCard>
  )
}
