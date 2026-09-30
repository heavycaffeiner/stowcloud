import type { ChangeEvent } from 'react'
import { useEffect, useRef } from 'react'

export interface CheckboxProps {
  checked?: boolean
  indeterminate?: boolean
  label?: string
  hideLabel?: boolean
  disabled?: boolean
  required?: boolean
  name?: string
  value?: string
  onchange?: (checked: boolean) => void
  onChange?: (checked: boolean) => void
}

export function Checkbox({
  checked = false,
  indeterminate = false,
  label,
  hideLabel = false,
  disabled = false,
  required = false,
  name,
  value,
  onchange,
  onChange
}: CheckboxProps) {
  const ref = useRef<HTMLInputElement>(null)
  useEffect(() => {
    if (ref.current) ref.current.indeterminate = indeterminate
  }, [indeterminate])
  const handleChange = (event: ChangeEvent<HTMLInputElement>) => {
    onchange?.(event.currentTarget.checked)
    onChange?.(event.currentTarget.checked)
  }
  return (
    <label className="sc-checkbox">
      <mdui-checkbox
        checked={checked}
        indeterminate={indeterminate}
        disabled={disabled}
        required={required}
        name={name}
        value={value}
        onChange={handleChange}
        aria-label={label}
      />
      {label && !hideLabel ? <span>{label}</span> : null}
    </label>
  )
}
