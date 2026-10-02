// The preview's own read: a ZIP on an encrypted share, which the browser lists
// because the server holds no key to do it.
import { useQuery } from '@tanstack/react-query'
import { listEncryptedArchive } from './zip-listing'
import type { ShareEncryption } from '../shares/api'
import type { Entry } from '../file-browser/api'

/** `unlocked` is in the key so a listing that failed while locked is retried
 *  once the share is unlocked. */
export function useEncryptedArchive(
  entry: Entry | null,
  encryption: ShareEncryption | null,
  unlocked: boolean,
  enabled: boolean
) {
  return useQuery({
    queryKey: ['preview-encrypted-archive', entry?.path ?? '', unlocked],
    queryFn: () => listEncryptedArchive(entry as Entry, (encryption as ShareEncryption).salt),
    enabled: enabled && entry !== null && encryption !== null,
    staleTime: Infinity
  })
}
