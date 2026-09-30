import 'mdui/components/text-field.js'
import type { KeyboardEventHandler } from 'react'
import { useEffect, useRef } from 'react'
import { cx } from './cx'
import * as styles from './TextField.css'

interface TextFieldElement extends HTMLElement {
  value: string
  updateComplete?: Promise<unknown>
  setAttribute(name: string, value: string): void
  removeAttribute(name: string): void
}

// The input lives in mdui's shadow root, where an id reference to the light-DOM error text
// would not resolve, so the error is linked by element reflection instead.
function syncTextFieldAria(
  element: TextFieldElement | null,
  label: string | undefined,
  errorElement: HTMLElement | null
): void {
  if (!element) return
  const apply = () => {
    const control = element.shadowRoot?.querySelector<HTMLElement>('[part="input"]')
    if (!control) return
    if (label) control.setAttribute('aria-label', label)
    else control.removeAttribute('aria-label')
    if (errorElement) control.setAttribute('aria-invalid', 'true')
    else control.removeAttribute('aria-invalid')
    control.ariaDescribedByElements = errorElement ? [errorElement] : null
  }
  if (element.updateComplete) void element.updateComplete.then(apply)
  else queueMicrotask(apply)
}

export type TextFieldType = 'text' | 'search' | 'password' | 'date' | 'datetime-local' | 'number'

export interface TextFieldProps {
  value?: string
  label?: string
  placeholder?: string
  helper?: string
  variant?: 'filled' | 'outlined'
  type?: TextFieldType
  error?: string | null
  min?: number
  max?: number
  list?: string
  autoFocus?: boolean
  autoComplete?: string
  disabled?: boolean
  required?: boolean
  name?: string
  className?: string
  onValueChange?: (value: string) => void
  onKeyDown?: KeyboardEventHandler<HTMLElement>
}

export function TextField({
  value = '',
  helper,
  label,
  placeholder,
  variant = 'outlined',
  type = 'text',
  error = null,
  min,
  max,
  list,
  autoFocus = false,
  autoComplete,
  disabled = false,
  required = false,
  name,
  className,
  onValueChange,
  onKeyDown
}: TextFieldProps) {
  const ref = useRef<TextFieldElement | null>(null)
  const errorRef = useRef<HTMLParagraphElement | null>(null)
  useEffect(() => {
    syncTextFieldAria(ref.current, label, error ? errorRef.current : null)
  }, [label, error])

  useEffect(() => {
    const element = ref.current
    if (element) element.value = value
  }, [value])

  useEffect(() => {
    const element = ref.current
    if (!element) return
    const handleInput = () => {
      if (onValueChange) onValueChange(element.value)
    }
    element.addEventListener('input', handleInput)
    return () => element.removeEventListener('input', handleInput)
  }, [onValueChange])
  useEffect(() => {
    const element = ref.current
    if (!element) return
    if (list) element.setAttribute('list', list)
    else element.removeAttribute('list')
  }, [list])

  useEffect(() => {
    const element = ref.current
    if (!element || !autoFocus) return
    let cancelled = false
    const focus = () => {
      if (!cancelled) element.focus()
    }
    const updateComplete = element.updateComplete
    if (updateComplete && typeof updateComplete.then === 'function') {
      void updateComplete.then(focus)
    } else {
      queueMicrotask(focus)
    }
    return () => {
      cancelled = true
    }
  }, [autoFocus])

  return (
    <div className={cx(styles.root, className)}>
      <mdui-text-field
        ref={ref}
        className={styles.input}
        variant={variant}
        label={label}
        placeholder={placeholder}
        type={type}
        min={min}
        max={max}
        autocomplete={autoComplete}
        autofocus={autoFocus || undefined}
        disabled={disabled}
        required={required}
        name={name}
        helper={error ? undefined : helper}
        onKeyDown={onKeyDown}
      ></mdui-text-field>
      {error ? (
        <p ref={errorRef} className={styles.error} role="alert">
          {error}
        </p>
      ) : null}
    </div>
  )
}
