import type { MouseEventHandler, ReactNode } from 'react'

export interface ListItemProps {
  selected?: boolean
  onClick?: MouseEventHandler<HTMLDivElement>
  onclick?: MouseEventHandler<HTMLDivElement>
  leading?: ReactNode
  trailing?: ReactNode
  headline: ReactNode
  supporting?: ReactNode
}

export function ListItem({ selected = false, onClick, onclick, leading, trailing, headline, supporting }: ListItemProps) {
  const action = onClick ?? onclick
  return (
    <div
      className={`sc-list-item${selected ? ' sc-list-item--selected' : ''}${action ? ' sc-list-item--clickable' : ''}`}
      onClick={action}
      role={action ? 'button' : 'presentation'}
      tabIndex={action ? 0 : undefined}
      onKeyDown={action ? (event) => { if (event.key === 'Enter' || event.key === ' ') { event.preventDefault(); action(event as unknown as React.MouseEvent<HTMLDivElement>) } } : undefined}
    >
      {leading ? <span className="sc-list-item__leading">{leading}</span> : null}
      <span className="sc-list-item__text">
        <span className="sc-list-item__headline">{headline}</span>
        {supporting ? <span className="sc-list-item__supporting">{supporting}</span> : null}
      </span>
      {trailing ? <span className="sc-list-item__trailing">{trailing}</span> : null}
    </div>
  )
}
