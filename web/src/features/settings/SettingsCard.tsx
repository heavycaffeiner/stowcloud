import { createElement, type ReactNode } from 'react'

interface SettingsCardProps {
  title: ReactNode
  description?: ReactNode
  leading?: ReactNode
  trailing?: ReactNode
  headingLevel?: 2 | 3 | 4 | 5 | 6
  children: ReactNode
}

export function SettingsCard({ title, description, leading, trailing, headingLevel = 2, children }: SettingsCardProps) {
  const heading = `h${headingLevel}` as 'h2' | 'h3' | 'h4' | 'h5' | 'h6'
  return (
    <article className="sc-settings-card">
      <div className="sc-settings-card__head">
        {leading}
        <div className="sc-settings-card__meta">
          {createElement(heading, null, title)}
          {description}
        </div>
        {trailing}
      </div>
      {children}
    </article>
  )
}
