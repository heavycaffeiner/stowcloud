import { useEffect, useRef } from 'react'

export interface SelectOption {
  value: string
  text?: string
  label?: string
  disabled?: boolean
}

export interface SelectProps {
  value?: string
  label?: string
  options: SelectOption[]
  testid?: string
  id?: string
  name?: string
  disabled?: boolean
  required?: boolean
  ariaLabel?: string
  ariaDescribedby?: string
  onValueChange?: (value: string) => void
  onChange?: (value: string) => void
}

interface SelectElement extends HTMLElement {
  value: string
  updateComplete?: Promise<unknown>
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
  ariaLabel,
  ariaDescribedby,
  onValueChange,
  onChange
}: SelectProps) {
  const ref = useRef<SelectElement | null>(null)

  useEffect(() => {
    const element = ref.current
    if (element && element.value !== value) {
      element.value = value
    }
  }, [value])

  useEffect(() => {
    const element = ref.current
    if (!element) return
    const handleChange = () => {
      const next = element.value
      onValueChange?.(next)
      onChange?.(next)
    }
    const handleClose = (event: Event) => {
      event.stopPropagation()
    }
    element.addEventListener('change', handleChange)
    element.addEventListener('close', handleClose)
    return () => {
      element.removeEventListener('change', handleChange)
      element.removeEventListener('close', handleClose)
    }
  }, [onValueChange, onChange])

  return (
    <div className="sc-select">
      <mdui-select
        ref={ref}
        variant="outlined"
        value={value}
        label={label}
        id={id}
        name={name}
        disabled={disabled}
        required={required}
        data-testid={testid}
        aria-label={ariaLabel ?? label}
        aria-describedby={ariaDescribedby}
        style={{ width: '100%' }}
      >
        {options.map((option) => (
          <mdui-menu-item key={option.value} value={option.value} disabled={option.disabled}>
            {option.text ?? option.label ?? option.value}
          </mdui-menu-item>
        ))}
      </mdui-select>
    </div>
  )
}
