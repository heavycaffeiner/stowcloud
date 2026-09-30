import type { ReactNode } from 'react'

export interface ListItemProps {
  selected?: boolean
  onClick?: () => void
  leading?: ReactNode
  trailing?: ReactNode
  headline: ReactNode
  supporting?: ReactNode
}

export function ListItem({ selected = false, onClick, leading, trailing, headline, supporting }: ListItemProps) {
  return (
    <div
      className={`sc-list-item${selected ? ' sc-list-item-selected' : ''}${onClick ? ' sc-list-item-clickable' : ''}`}
      onClick={onClick ? () => onClick() : undefined}
      role={onClick ? 'button' : 'presentation'}
      tabIndex={onClick ? 0 : undefined}
      onKeyDown={
        onClick
          ? (event) => {
              if (event.key === 'Enter' || event.key === ' ') {
                event.preventDefault()
                onClick()
              }
            }
          : undefined
      }
    >
      {leading ? <span className="sc-list-item-leading">{leading}</span> : null}
      <span className="sc-list-item-text">
        <span className="sc-list-item-headline">{headline}</span>
        {supporting ? <span className="sc-list-item-supporting">{supporting}</span> : null}
      </span>
      {trailing ? <span className="sc-list-item-trailing">{trailing}</span> : null}
    </div>
  )
}
