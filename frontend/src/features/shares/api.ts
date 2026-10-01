// Which shares are end-to-end encrypted, as the server describes them.
import { useQuery } from '@tanstack/react-query'
import { client, unwrap } from '../../api/fetcher'
import type { components } from '../../api/generated/schema'
import { keys } from '../../lib/query/keys'

/** One share's encryption parameters. The server holds no key: `salt` is the
 *  string the user types into rclone as `password2`, and `verifier` is base64.
 *  `labels` is a per-account projection (one share can surface under several
 *  grant labels), so it must never be cached across accounts. */
export interface ShareEncryption {
  share: number
  labels: string[]
  scheme: string
  salt: string
  verifier: string
  createdNs: number
}

function shareEncryptionFromWire(w: components['schemas']['ShareEncryptionView']): ShareEncryption {
  return {
    share: w.share,
    labels: w.labels ?? [],
    scheme: w.scheme,
    salt: w.salt,
    verifier: w.verifier,
    createdNs: w.created_ns
  }
}

/** Every encrypted share the caller holds a grant on. Any account may ask: an
 *  upload has to know before it writes a byte. */
export async function fetchShareEncryptions(): Promise<ShareEncryption[]> {
  const out = await unwrap(client.GET('/api/v1/encryption'))
  return (out.shares ?? []).map(shareEncryptionFromWire)
}

export function useShareEncryptions() {
  return useQuery({ queryKey: keys.shareEncryptions(), queryFn: fetchShareEncryptions })
}
