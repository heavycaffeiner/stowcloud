import type { MouseEventHandler, ReactNode } from 'react'
import { useEffect, useRef, useState } from 'react'
import { Icon } from './Icon'

interface IconButtonElement extends HTMLElement {
  updateComplete?: Promise<unknown>
}

function syncIconButtonAria(element: IconButtonElement | null, label: string, expanded: boolean | undefined, selected: boolean | undefined): void {
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
  onClick
}: IconButtonProps) {
  const buttonRef = useRef<IconButtonElement | null>(null)
  useEffect(() => { syncIconButtonAria(buttonRef.current, label, expanded, selected) }, [label, expanded, selected])

  const wrapperRef = useRef<HTMLSpanElement>(null)
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const [shown, setShown] = useState(false)
  const [position, setPosition] = useState({ left: 0, top: 0 })

  const clearTimer = () => {
    if (timerRef.current !== null) {
      clearTimeout(timerRef.current)
      timerRef.current = null
    }
  }
  const hide = () => {
    clearTimer()
    setShown(false)
  }
  const place = () => {
    const button = wrapperRef.current?.querySelector<HTMLElement>('mdui-button-icon')
    const rect = button?.getBoundingClientRect()
    if (!rect) return
    setPosition({ left: rect.left + rect.width / 2, top: rect.bottom + EDGE_MARGIN_PX })
  }
  const show = (delay: number) => {
    if (disabled) return
    clearTimer()
    timerRef.current = setTimeout(() => {
      timerRef.current = null
      place()
      setShown(true)
    }, delay)
  }

  useEffect(() => {
    const wrapper = wrapperRef.current
    if (!wrapper) return
    const onFocusIn = (event: FocusEvent) => {
      const target = event.target
      if (target instanceof Element) {
        try {
          if (target.matches(':focus-visible')) show(0)
        } catch {
          // Older DOM implementations do not expose :focus-visible.
        }
      }
    }
    const onFocusOut = () => hide()
    const onPointerEnter = () => show(HOVER_DELAY_MS)
    const onPointerLeave = () => hide()
    const onPointerDown = () => hide()
    wrapper.addEventListener('focusin', onFocusIn)
    wrapper.addEventListener('focusout', onFocusOut)
    wrapper.addEventListener('pointerenter', onPointerEnter)
    wrapper.addEventListener('pointerleave', onPointerLeave)
    wrapper.addEventListener('pointerdown', onPointerDown)
    return () => {
      wrapper.removeEventListener('focusin', onFocusIn)
      wrapper.removeEventListener('focusout', onFocusOut)
      wrapper.removeEventListener('pointerenter', onPointerEnter)
      wrapper.removeEventListener('pointerleave', onPointerLeave)
      wrapper.removeEventListener('pointerdown', onPointerDown)
      clearTimer()
    }
  }, [disabled])

  useEffect(() => {
    if (!shown) return
    const onScroll = () => hide()
    const onResize = () => hide()
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') hide()
    }
    window.addEventListener('scroll', onScroll, { passive: true, capture: true })
    window.addEventListener('resize', onResize)
    window.addEventListener('keydown', onKeyDown)
    return () => {
      window.removeEventListener('scroll', onScroll, { capture: true })
      window.removeEventListener('resize', onResize)
      window.removeEventListener('keydown', onKeyDown)
    }
  }, [shown])

  const currentIcon = selected && selectedIcon ? selectedIcon : icon
  return (
    <span ref={wrapperRef} className="sc-icon-button" role="none">
      <mdui-button-icon
        ref={buttonRef}
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
          className="sc-icon-button__tip sc-icon-button__tip--placed"
          role="tooltip"
          aria-hidden="true"
          style={{ left: Math.max(EDGE_MARGIN_PX, Math.min(position.left, window.innerWidth - EDGE_MARGIN_PX)), top: position.top }}
        >
          {label}
        </span>
      ) : null}
    </span>
  )
}
