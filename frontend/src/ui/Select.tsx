import 'mdui/components/select.js'
import 'mdui/components/menu-item.js'
import { useEffect, useRef } from 'react'
import * as styles from './Select.css'

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
  onValueChange?: (value: string) => void
  onChange?: (value: string) => void
}

interface SelectElement extends HTMLElement {
  value: string
  updateComplete?: Promise<unknown>
}

// The host's aria-label does not reach the input mdui nests two shadow roots deep, so name that input directly.
async function syncSelectAria(element: SelectElement, label: string | undefined): Promise<void> {
  await element.updateComplete
  const field = element.shadowRoot?.querySelector<SelectElement>('[part="text-field"]')
  await field?.updateComplete
  const control = field?.shadowRoot?.querySelector<HTMLElement>('[part="input"]')
  if (!control) return
  if (label) control.setAttribute('aria-label', label)
  else control.removeAttribute('aria-label')
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

  const accessibleName = ariaLabel ?? label
  useEffect(() => {
    if (ref.current) void syncSelectAria(ref.current, accessibleName)
  }, [accessibleName])

  useEffect(() => {
    const element = ref.current
    if (!element) return
    const handleChange = () => {
      const next = element.value
      onValueChange?.(next)
      onChange?.(next)
    }
    element.addEventListener('change', handleChange)
    return () => element.removeEventListener('change', handleChange)
  }, [onValueChange, onChange])

  return (
    <div className={styles.root}>
      <mdui-select
        ref={ref}
        className={styles.control}
        variant="outlined"
        value={value}
        label={label}
        id={id}
        name={name}
        disabled={disabled}
        required={required}
        data-testid={testid}
        aria-label={accessibleName}
        style={{ width: '100%' }}
        onKeyDown={(event) => {
          // The open list closes on Escape by itself; marking the key handled keeps an enclosing dialog open.
          const dropdown = ref.current?.shadowRoot?.querySelector<HTMLElement & { open?: boolean }>('mdui-dropdown')
          if (event.key === 'Escape' && dropdown?.open) event.preventDefault()
        }}
      >
        {options.map((option) => (
          <mdui-menu-item key={option.value} className={styles.item} value={option.value} disabled={option.disabled}>
            {option.text ?? option.label ?? option.value}
          </mdui-menu-item>
        ))}
      </mdui-select>
    </div>
  )
}
