import { useEffect, useState } from 'react'

export function useDebounced<T>(value: T, milliseconds: number): T {
  type DebouncedState = { value: T }
  const [state, setState] = useState<DebouncedState>({ value })
  const setSettled = (next: T): void => setState({ value: next })
  useEffect(() => {
    const timer = window.setTimeout(() => setSettled(value), milliseconds)
    return () => window.clearTimeout(timer)
  }, [milliseconds, value])
  return state.value
}
