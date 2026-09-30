import 'mdui/components/button-icon.js'
import type { MouseEventHandler, ReactNode } from 'react'
import { useEffect, useRef, useState } from 'react'
import { useEventListener } from '../hooks/use-event-listener'
import { cx } from './cx'
import { Icon } from './Icon'
import * as styles from './IconButton.css'

interface IconButtonElement extends HTMLElement {
  updateComplete?: Promise<unknown>
}

function syncIconButtonAria(
  element: IconButtonElement | null,
  label: string,
  expanded: boolean | undefined,
  selected: boolean | undefined
): void {
  if (!element) return
  const apply = () => {
    const control = element.shadowRoot?.querySelector<HTMLElement>('[part="button"]')
    if (!control) return
    control.setAttribute('aria-label', label)
    if (expanded === undefined) control.removeAttribute('aria-expanded')
    else control.setAttribute('aria-expanded', String(expanded))
    if (expanded === undefined && selected !== undefined) control.setAttribute('aria-pressed', String(selected))
    else control.removeAttribute('aria-pressed')
  }
  if (element.updateComplete) void element.updateComplete.then(apply)
  else queueMicrotask(apply)
}
export interface IconButtonProps {
  label: string
  selected?: boolean
  expanded?: boolean
  disabled?: boolean
  icon?: string
  selectedIcon?: string
  children?: ReactNode
  className?: string
  onClick?: MouseEventHandler<HTMLElement>
}

const HOVER_DELAY_MS = 400
const EDGE_MARGIN_PX = 8

export function IconButton({
  label,
  selected,
  expanded,
  disabled = false,
  icon,
  selectedIcon,
  children,
  className,
  onClick
}: IconButtonProps) {
  const buttonRef = useRef<IconButtonElement | null>(null)
  useEffect(() => {
    syncIconButtonAria(buttonRef.current, label, expanded, selected)
  }, [label, expanded, selected])

  const wrapperRef = useRef<HTMLSpanElement>(null)
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const [tooltip, setTooltip] = useState({ shown: false, position: { left: 0, top: 0 } })
  const { shown, position } = tooltip

  const clearTimer = () => {
    if (timerRef.current !== null) {
      clearTimeout(timerRef.current)
      timerRef.current = null
    }
  }
  const hide = () => {
    clearTimer()
    setTooltip((state) => ({ ...state, shown: false }))
  }
  const place = () => {
    const button = wrapperRef.current?.querySelector<HTMLElement>('mdui-button-icon')
    const rect = button?.getBoundingClientRect()
    if (!rect) return
    setTooltip((state) => ({
      ...state,
      position: { left: rect.left + rect.width / 2, top: rect.bottom + EDGE_MARGIN_PX }
    }))
  }
  const show = (delay: number) => {
    if (disabled) return
    clearTimer()
    timerRef.current = setTimeout(() => {
      timerRef.current = null
      place()
      setTooltip((state) => ({ ...state, shown: true }))
    }, delay)
  }

  const showOnKeyboardFocus = (target: EventTarget): void => {
    if (!(target instanceof Element)) return
    try {
      if (target.matches(':focus-visible')) show(0)
    } catch {
      // Older DOM implementations do not expose :focus-visible.
    }
  }
  useEffect(
    () => () => {
      if (timerRef.current !== null) clearTimeout(timerRef.current)
    },
    []
  )

  const tipTarget = shown ? window : null
  useEventListener(tipTarget, 'scroll', hide, { capture: true, passive: true })
  useEventListener(tipTarget, 'resize', hide)
  useEventListener(tipTarget, 'keydown', (event) => {
    if (event.key === 'Escape') hide()
  })

  const currentIcon = selected && selectedIcon ? selectedIcon : icon
  return (
    <span
      ref={wrapperRef}
      className={cx(styles.root, className)}
      role="none"
      onFocus={(event) => showOnKeyboardFocus(event.target)}
      onBlur={hide}
      onPointerEnter={() => show(HOVER_DELAY_MS)}
      onPointerLeave={hide}
      onPointerDown={hide}
    >
      <mdui-button-icon
        ref={buttonRef}
        className={styles.button}
        variant={selected ? 'tonal' : 'standard'}
        selectable={selected !== undefined}
        selected={selected}
        disabled={disabled}
        aria-label={label}
        aria-expanded={expanded === undefined ? undefined : expanded}
        aria-pressed={expanded === undefined && selected !== undefined ? selected : undefined}
        onClick={onClick}
      >
        {currentIcon ? <Icon name={currentIcon} /> : children}
      </mdui-button-icon>
      {shown ? (
        <span
          className={cx(styles.tip, styles.tipPlaced)}
          role="tooltip"
          aria-hidden="true"
          style={{
            left: Math.max(EDGE_MARGIN_PX, Math.min(position.left, window.innerWidth - EDGE_MARGIN_PX)),
            top: position.top
          }}
        >
          {label}
        </span>
      ) : null}
    </span>
  )
}
