import 'mdui/components/checkbox.js'
import { useRef } from 'react'

export interface CheckboxProps {
  checked?: boolean
  indeterminate?: boolean
  label?: string
  hideLabel?: boolean
  disabled?: boolean
  required?: boolean
  name?: string
  value?: string
  onChange?: (checked: boolean) => void
}

interface CheckboxElement extends HTMLElement {
  checked: boolean
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
  onChange
}: CheckboxProps) {
  const ref = useRef<CheckboxElement>(null)
  return (
    <label className="sc-checkbox">
      <mdui-checkbox
        ref={ref}
        checked={checked}
        indeterminate={indeterminate}
        disabled={disabled}
        required={required}
        name={name}
        value={value}
        onChange={() => {
          if (ref.current) onChange?.(ref.current.checked)
        }}
        aria-label={label}
      />
      {label && !hideLabel ? <span>{label}</span> : null}
    </label>
  )
}

/** Shows a selection state without taking input or focus; the surrounding control does both. */
export function CheckboxIndicator({ checked, label }: { checked: boolean; label: string }) {
  return (
    <mdui-checkbox checked={checked} tabIndex={-1}>
      <span className="sc-sr-only">{label}</span>
    </mdui-checkbox>
  )
}
