import { useMediaQuery } from '../../hooks/use-media-query'

// The complement of the (min-width: 905px) breakpoint the stylesheets use for the wide layout.
const COMPACT_QUERY = 'not all and (min-width: 905px)'

/** Whether the viewport is in the compact layout used by navigation and file controls. */
export function useCompact(): boolean {
  return useMediaQuery(COMPACT_QUERY)
}
