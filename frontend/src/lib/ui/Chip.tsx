import type { MouseEventHandler, ReactNode } from 'react'
import { useEffect, useRef } from 'react'
export interface ChipProps {
  variant?: 'assist' | 'filter' | 'input'
  selected?: boolean
  onClick?: MouseEventHandler<HTMLElement>
  onclick?: MouseEventHandler<HTMLElement>
  onRemove?: () => void
  onremove?: () => void
  ariaLabel?: string
  children?: ReactNode
}

export function Chip({
  variant = 'assist',
  selected = false,
  onClick,
  onclick,
  onRemove,
  onremove,
  ariaLabel,
  children
}: ChipProps) {
  const remove = onRemove ?? onremove
  const action = onClick ?? onclick ?? (remove ? () => remove() : undefined)
  const ref = useRef<HTMLElement | null>(null)
  useEffect(() => {
    const element = ref.current
    if (!element || !remove) return
    const handler = () => remove()
    element.addEventListener('delete', handler)
    return () => element.removeEventListener('delete', handler)
  }, [remove])
  return (
    <mdui-chip
      ref={ref}
      variant={variant === 'filter' ? 'filter' : variant}
      selected={selected}
      selectable={Boolean(onClick ?? onclick)}
      deletable={Boolean(remove)}
      aria-label={ariaLabel}
      onClick={action}
    >
      {children}
    </mdui-chip>
  )
}
