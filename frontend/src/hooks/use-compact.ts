import { media } from '@/shared/theme'
import { useMediaQuery } from './use-media-query'

/** Whether the viewport is in the compact layout used by navigation and file controls. */
export function useCompact(): boolean {
  return useMediaQuery(media.notWide)
}
