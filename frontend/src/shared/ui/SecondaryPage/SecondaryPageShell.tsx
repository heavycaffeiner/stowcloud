import type { PropsWithChildren, ReactNode } from 'react'
import { cx } from '../cx'
import { StowIconButton } from '../IconButton'
import * as styles from './SecondaryPage.css'

export interface SecondaryPageShellProps extends PropsWithChildren {
  title: ReactNode
  refreshLabel: string
  onRefresh: () => void
  className?: string
}

/** The frame of a secondary route: one heading with a refresh action above the content. */
export function SecondaryPageShell({ title, refreshLabel, onRefresh, className, children }: SecondaryPageShellProps) {
  return (
    <section className={cx(styles.root, className)}>
      <div className={styles.inner}>
        <header className={styles.header}>
          <h1 className={styles.title}>{title}</h1>
          <StowIconButton label={refreshLabel} icon="refresh" onClick={onRefresh} />
        </header>
        {children}
      </div>
    </section>
  )
}
