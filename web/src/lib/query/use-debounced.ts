import { useEffect, useState } from 'react'

export function useDebounced<T>(value: T, milliseconds: number): T {
  const [settled, setSettled] = useState(value)

  useEffect(() => {
    const timer = window.setTimeout(() => setSettled(value), milliseconds)
    return () => window.clearTimeout(timer)
  }, [milliseconds, value])

  return settled
}
