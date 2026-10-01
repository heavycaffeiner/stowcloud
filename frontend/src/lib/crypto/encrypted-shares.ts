// Which shares this account sees are end-to-end encrypted, fetched once per
// session and cached by the label a path is addressed under. The set is only
// correct for the signed-in account, so it lives in memory and is dropped on
// logout.
import { fetchShareEncryptions, type ShareEncryption } from '../../features/shares/api'

let cache: Promise<ShareEncryption[]> | null = null

/** Fetches the whole set once and reuses it for every later call, until a
 *  caller clears it with `invalidateEncryptedShares`: the admin UI after it
 *  turns encryption on or off, and the logout mutation, since a stale set
 *  would otherwise leak into the next signed-in account. A rejected fetch is
 *  not cached: the next call tries again rather than remembering a transient
 *  failure as "the set is empty".
 */
export function encryptedShares(): Promise<ShareEncryption[]> {
  if (cache === null) {
    cache = fetchShareEncryptions()
    cache.catch(() => {
      cache = null
    })
  }
  return cache
}

/** Drops the cached set. Call on logout (a different account's `labels`
 *  projection must never leak into the next session) and after the admin UI
 *  enables or disables encryption for a share it can see. */
export function invalidateEncryptedShares(): void {
  cache = null
}

/** The first path segment of a vpath (`/label/rest` or `label/rest`): the
 *  share label every destination and every listed entry is addressed by. */
export function shareLabelOf(vpath: string): string {
  const trimmed = vpath.startsWith('/') ? vpath.slice(1) : vpath
  const cut = trimmed.indexOf('/')
  return cut < 0 ? trimmed : trimmed.slice(0, cut)
}

/**
 * This label's encryption row, or `null` once a successful fetch confirms
 * the label names no encrypted share.
 *
 * Fails closed: a fetch that cannot complete rejects rather than resolving
 * to "unencrypted". A caller deciding whether to send plaintext MUST NOT
 * catch this and fall back to an unencrypted upload; the whole point of the
 * guarantee is that a share's encryption state is never guessed at.
 */
export async function encryptionForLabel(label: string): Promise<ShareEncryption | null> {
  const shares = await encryptedShares()
  return shares.find((s) => s.labels.includes(label)) ?? null
}
