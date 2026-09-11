import type { KeyboardEventHandler } from 'react'
import { useEffect, useId, useRef } from 'react'

interface TextFieldElement extends HTMLElement {
  value: string
  updateComplete?: Promise<unknown>
  setAttribute(name: string, value: string): void
  removeAttribute(name: string): void
}

function syncTextFieldAria(element: TextFieldElement | null, label: string | undefined, invalid: boolean, describedBy: string | undefined): void {
  if (!element) return
  const apply = () => {
    const control = element.shadowRoot?.querySelector<HTMLElement>('[part="input"]')
    if (!control) return
    if (label) control.setAttribute('aria-label', label)
    else control.removeAttribute('aria-label')
    if (invalid) control.setAttribute('aria-invalid', 'true')
    else control.removeAttribute('aria-invalid')
    if (describedBy) control.setAttribute('aria-describedby', describedBy)
    else control.removeAttribute('aria-describedby')
  }
  if (element.updateComplete) void element.updateComplete.then(apply)
  else queueMicrotask(apply)
}

export type TextFieldType = 'text' | 'search' | 'password' | 'date' | 'datetime-local' | 'number'

export interface TextFieldProps {
  value?: string
  label?: string
  placeholder?: string
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
  ariaDescribedby?: string
  onValueChange?: (value: string) => void
  onKeyDown?: KeyboardEventHandler<HTMLElement>
}


export function TextField({
  value = '',
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
  ariaDescribedby,
  onValueChange,
  onKeyDown
}: TextFieldProps) {
  const ref = useRef<TextFieldElement | null>(null)
  const generatedId = useId()
  const errorId = `${generatedId}-error`
  const describedBy = [ariaDescribedby, error ? errorId : null].filter(Boolean).join(' ') || undefined
  useEffect(() => { syncTextFieldAria(ref.current, label, Boolean(error), describedBy) }, [label, error, describedBy])

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
    <div className="sc-field">
      <mdui-text-field
        ref={ref}
        variant={variant}
        label={label}
        placeholder={placeholder}
        type={type}
        min={min}
        max={max}
        autocomplete={autoComplete}
        disabled={disabled}
        required={required}
        name={name}
        aria-invalid={error ? 'true' : undefined}
        aria-describedby={describedBy}
        onKeyDown={onKeyDown}
      ></mdui-text-field>
      {error ? (
        <p id={errorId} className="sc-field__error" role="alert">
          {error}
        </p>
      ) : null}
    </div>
  )
}
