import type { ReactNode } from 'react'
import { useEffect, useRef } from 'react'

export interface SwitchProps {
  checked?: boolean
  disabled?: boolean
  label?: string
  showLabel?: boolean
  required?: boolean
  name?: string
  value?: string
  onChange?: (checked: boolean) => void
  children?: ReactNode
}

interface SwitchElement extends HTMLElement {
  checked: boolean
  updateComplete?: Promise<unknown>
}

export function Switch({
  checked = false,
  disabled = false,
  label,
  showLabel = true,
  required = false,
  name,
  value,
  onChange,
  children
}: SwitchProps) {
  const ref = useRef<SwitchElement | null>(null)

  useEffect(() => {
    if (ref.current) ref.current.checked = checked
  }, [checked])

  useEffect(() => {
    const element = ref.current
    if (!element) return
    const propose = () => {
      const wanted = element.checked
      element.checked = checked
      onChange?.(wanted)
    }
    element.addEventListener('change', propose)
    return () => element.removeEventListener('change', propose)
  }, [checked, onChange])

  const accessibleLabel = label ?? (typeof children === 'string' ? children : undefined)
  return (
    <label className={`sc-switch-row${disabled ? ' sc-switch-row--disabled' : ''}`}>
      <mdui-switch
        ref={ref}
        checked={checked}
        disabled={disabled}
        required={required}
        name={name}
        value={value}
        aria-label={accessibleLabel}
      ></mdui-switch>
      {showLabel && label ? <span>{label}</span> : null}
      {children && typeof children !== 'string' ? children : null}
    </label>
  )
}
