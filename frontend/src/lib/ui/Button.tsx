import type { MouseEventHandler, PropsWithChildren, ReactNode } from 'react'
import { useEffect, useRef } from 'react'
import { useI18n } from '../i18n/use-i18n'

interface ButtonElement extends HTMLElement {
  updateComplete?: Promise<unknown>
}

function syncButtonAria(element: ButtonElement | null, ariaLabel: string | undefined, pressed: boolean | undefined, loading: boolean): void {
  if (!element) return
  const apply = () => {
    const control = element.shadowRoot?.querySelector<HTMLElement>('[part="button"]')
    if (!control) return
    if (ariaLabel) control.setAttribute('aria-label', ariaLabel)
    else control.removeAttribute('aria-label')
    if (pressed === undefined) control.removeAttribute('aria-pressed')
    else control.setAttribute('aria-pressed', String(pressed))
    if (loading) control.setAttribute('aria-busy', 'true')
    else control.removeAttribute('aria-busy')
  }
  if (element.updateComplete) void element.updateComplete.then(apply)
  else queueMicrotask(apply)
}
export interface ButtonProps extends PropsWithChildren {
  variant?: 'elevated' | 'filled' | 'tonal' | 'outlined' | 'text'
  type?: 'button' | 'submit' | 'reset'
  disabled?: boolean
  loading?: boolean
  danger?: boolean
  square?: boolean
  pressed?: boolean
  ariaLabel?: string
  name?: string
  value?: string
  form?: string
  icon?: ReactNode
  endIcon?: ReactNode
  onClick?: MouseEventHandler<HTMLElement>
}

export function Button({
  variant = 'filled',
  type = 'button',
  disabled = false,
  loading = false,
  danger = false,
  square = false,
  pressed,
  ariaLabel,
  name,
  value,
  form,
  icon,
  endIcon,
  onClick,
  children
}: ButtonProps) {
  const { t } = useI18n()
  const ref = useRef<ButtonElement | null>(null)
  useEffect(() => { syncButtonAria(ref.current, ariaLabel, pressed, loading) }, [ariaLabel, pressed, loading])
  const squareIcon = square ? (icon ?? children) : null
  return (
    <span className={`sc-button-wrap${danger ? ' sc-danger' : ''}`}>
      <mdui-button
        ref={ref}
        variant={variant}
        type={type}
        disabled={disabled || loading}
        loading={loading}
        name={name}
        value={value}
        form={form}
        aria-label={ariaLabel}
        aria-pressed={pressed === undefined ? undefined : pressed}
        className={square ? 'sc-button--square' : undefined}
        aria-busy={loading ? 'true' : undefined}
        onClick={onClick}
      >
        {square
          ? squareIcon !== null && squareIcon !== undefined ? <span slot="icon">{squareIcon}</span> : null
          : icon ? <span slot="icon">{icon}</span> : null}
        {!square ? (loading ? <span className="sc-button__loading-label">{t('button.working')}</span> : children) : null}
        {!square && endIcon ? <span slot="end-icon">{endIcon}</span> : null}
      </mdui-button>
    </span>
  )
}
