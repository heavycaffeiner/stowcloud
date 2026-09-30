import 'mdui/components/button.js'
import type { MouseEventHandler, PropsWithChildren, ReactNode } from 'react'
import { useEffect, useRef } from 'react'
import { useI18n } from '../hooks/use-i18n'
import { cx } from './cx'
import * as styles from './Button.css'

interface ButtonElement extends HTMLElement {
  updateComplete?: Promise<unknown>
}

function syncButtonAria(
  element: ButtonElement | null,
  ariaLabel: string | undefined,
  pressed: boolean | undefined,
  loading: boolean
): void {
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
  className?: string
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
  className,
  onClick,
  children
}: ButtonProps) {
  const { t } = useI18n()
  const ref = useRef<ButtonElement | null>(null)
  useEffect(() => {
    syncButtonAria(ref.current, ariaLabel, pressed, loading)
  }, [ariaLabel, pressed, loading])
  const squareIcon = square ? (icon ?? children) : null
  return (
    <span className={cx(styles.wrap, danger && styles.danger, className)}>
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
        className={cx(styles.button, square && styles.square)}
        aria-busy={loading ? 'true' : undefined}
        onClick={onClick}
      >
        {square ? (
          squareIcon !== null && squareIcon !== undefined ? (
            <span slot="icon" className={styles.slot}>
              {squareIcon}
            </span>
          ) : null
        ) : icon ? (
          <span slot="icon" className={styles.slot}>
            {icon}
          </span>
        ) : null}
        {!square ? loading ? <span className={styles.loadingLabel}>{t('button.working')}</span> : children : null}
        {!square && endIcon ? (
          <span slot="end-icon" className={styles.slot}>
            {endIcon}
          </span>
        ) : null}
      </mdui-button>
    </span>
  )
}
