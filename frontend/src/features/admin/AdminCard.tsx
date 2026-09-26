import type { ReactNode } from 'react'

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
    <article className="sc-admin-card" id={id} aria-labelledby={headingId}>
      <div className="sc-admin-card-head">
        <div className="sc-admin-card-icon">{icon}</div>
        <div className="sc-admin-card-meta">
          <Heading className="sc-admin-card-title" id={headingId}>{title}</Heading>
          {subtitle ? <p className="sc-admin-card-subtitle">{subtitle}</p> : null}
        </div>
      </div>
      {bodyClassName ? <div className={bodyClassName}>{children}</div> : children}
    </article>
  )
}
