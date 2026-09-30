import 'mdui/components/chip.js'
import type { MouseEventHandler, ReactNode } from 'react'
import { useRef } from 'react'
import { useEventListener } from '../hooks/use-event-listener'
export interface ChipProps {
  variant?: 'assist' | 'filter' | 'input'
  selected?: boolean
  onClick?: MouseEventHandler<HTMLElement>
  onRemove?: () => void
  ariaLabel?: string
  children?: ReactNode
}

export function Chip({ variant = 'assist', selected = false, onClick, onRemove, ariaLabel, children }: ChipProps) {
  const ref = useRef<HTMLElement | null>(null)
  useEventListener(onRemove ? ref : null, 'delete', () => onRemove?.())
  return (
    <mdui-chip
      ref={ref}
      variant={variant === 'filter' ? 'filter' : variant}
      selected={selected}
      selectable={Boolean(onClick)}
      deletable={Boolean(onRemove)}
      aria-label={ariaLabel}
      onClick={onClick ?? (onRemove ? () => onRemove() : undefined)}
    >
      {children}
    </mdui-chip>
  )
}
