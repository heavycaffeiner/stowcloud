import type { ChangeEvent } from 'react'

export interface SelectOption {
  value: string
  text?: string
  label?: string
  disabled?: boolean
}

export interface SelectProps {
  value?: string
  label: string
  options: SelectOption[]
  testid?: string
  id?: string
  name?: string
  disabled?: boolean
  required?: boolean
  ariaDescribedby?: string
  onValueChange?: (value: string) => void
  onChange?: (value: string) => void
}

export function Select({
  value = '',
  label,
  options,
  testid,
  id,
  name,
  disabled = false,
  required = false,
  ariaDescribedby,
  onValueChange,
  onChange
}: SelectProps) {
  const handleChange = (event: ChangeEvent<HTMLSelectElement>) => {
    onValueChange?.(event.currentTarget.value)
    onChange?.(event.currentTarget.value)
  }
  return (
    <label className="sc-select">
      <span className="sc-select__label">{label}</span>
      <select
        value={value}
        id={id}
        name={name}
        disabled={disabled}
        required={required}
        data-testid={testid}
        aria-describedby={ariaDescribedby}
        onChange={handleChange}
      >
        {options.map((option) => (
          <option key={option.value} value={option.value} disabled={option.disabled}>
            {option.text ?? option.label ?? option.value}
          </option>
        ))}
      </select>
    </label>
  )
}
