import type { PropsWithChildren, ReactNode } from 'react'
import { Icon } from '../../../lib/ui/Icon'

export interface SecondaryPageShellProps extends PropsWithChildren {
  title: ReactNode
  refreshLabel: string
  onRefresh: () => void
  className?: string
  overlay?: ReactNode
}

/** Shared frame for secondary routes with one consistent, keyboard-accessible heading and refresh action. */
export function SecondaryPageShell({ title, refreshLabel, onRefresh, className, overlay, children }: SecondaryPageShellProps) {
  return (
    <section className={className ? `sc-secondary-page ${className}` : 'sc-secondary-page'}>
      <div className="sc-secondary-page-inner">
        <header className="sc-secondary-page-header">
          <h1>{title}</h1>
          <button type="button" className="sc-route-icon-button" aria-label={refreshLabel} onClick={onRefresh}><Icon name="refresh" /></button>
        </header>
        {children}
      </div>
      {overlay}
    </section>
  )
}
