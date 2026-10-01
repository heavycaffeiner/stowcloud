import type { PropsWithChildren, ReactNode } from 'react'
import { Icon } from './Icon'
import * as styles from './SecondaryPageShell.css'
import { cx } from './cx'

export interface SecondaryPageShellProps extends PropsWithChildren {
  title: ReactNode
  refreshLabel: string
  onRefresh: () => void
  className?: string
}

/** Shared frame for secondary routes with one consistent, keyboard-accessible heading and refresh action. */
export function SecondaryPageShell({ title, refreshLabel, onRefresh, className, children }: SecondaryPageShellProps) {
  return (
    <section className={cx(styles.root, className && className)}>
      <div className={styles.inner}>
        <header className={styles.header}>
          <h1 className={styles.title}>{title}</h1>
          <button type="button" className={styles.routeIconButton} aria-label={refreshLabel} onClick={onRefresh}>
            <Icon name="refresh" />
          </button>
        </header>
        {children}
      </div>
    </section>
  )
}
