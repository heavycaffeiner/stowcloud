import { useEffect } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'

/**
 * The tab named by the URL hash. A missing or unknown hash is rewritten to
 * `fallback`, and selecting a tab replaces the history entry instead of
 * pushing one.
 */
export function useHashTab<T extends string>(tabs: readonly T[], fallback: T): readonly [T, (next: T) => void] {
  const { pathname, search, hash } = useLocation()
  const navigate = useNavigate()
  const named = tabs.find((tab) => `#${tab}` === hash)
  const selectTab = (next: T): void => {
    if (`#${next}` !== hash) void navigate({ pathname, search, hash: next }, { replace: true })
  }
  useEffect(() => {
    if (named === undefined) void navigate({ pathname, search, hash: fallback }, { replace: true })
  }, [fallback, named, navigate, pathname, search])
  return [named ?? fallback, selectTab]
}
