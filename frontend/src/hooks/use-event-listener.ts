import { type RefObject, useEffect, useEffectEvent } from 'react'

type ListenerTarget = EventTarget | RefObject<EventTarget | null> | null

export interface ListenerOptions {
  readonly capture?: boolean
  readonly passive?: boolean
}

/**
 * Listens on `target` while it is non-null; pass null to detach. The listener
 * sees the latest render without rebinding. A ref target is read when the
 * effect runs, so it must point at an element that stays mounted.
 */
export function useEventListener<K extends keyof WindowEventMap>(
  target: Window | null,
  type: K,
  listener: (event: WindowEventMap[K]) => void,
  options?: ListenerOptions
): void
export function useEventListener<K extends keyof HTMLElementEventMap>(
  target: RefObject<HTMLElement | null> | null,
  type: K,
  listener: (event: HTMLElementEventMap[K]) => void,
  options?: ListenerOptions
): void
export function useEventListener(
  target: ListenerTarget,
  type: string,
  listener: (event: Event) => void,
  options?: ListenerOptions
): void
export function useEventListener(
  target: ListenerTarget,
  type: string,
  listener: (event: Event) => void,
  { capture = false, passive = false }: ListenerOptions = {}
): void {
  const onEvent = useEffectEvent(listener)
  useEffect(() => {
    const element = target instanceof EventTarget ? target : target?.current
    if (!element) return
    const handle = (event: Event): void => onEvent(event)
    element.addEventListener(type, handle, { capture, passive })
    return () => element.removeEventListener(type, handle, { capture })
  }, [target, type, capture, passive])
}
