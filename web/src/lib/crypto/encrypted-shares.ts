// web/src/lib/crypto/encrypted-shares.ts - which shares this account sees
// are end-to-end encrypted, fetched once per session and cached, keyed on
// the vpath label a destination or entry is addressed by.
//
// `ShareEncryption.labels` (web/src/lib/api/types.ts) is a per-caller
// projection, not a stored column: an account can hold two grants on the
// same share under two different subpaths, each surfaced under its own
// label, so the same share can appear under more than one label for this
// account and under entirely different labels for another. That is why this
// cache is a plain module-scoped variable rather than anything persisted:
// it is only ever correct for the account that is currently signed in, and
// must be dropped on logout so the next account does not inherit it.
//
// This module imports nothing from the api layer at runtime, and that is
// deliberate rather than incidental: `api/http.ts` reads the label logic
// below, so a runtime import back into `api/client.ts` would close a cycle
// through it, and `client.ts`'s eager `export const api = isMock ? mockApi :
// httpApi` evaluates before such a cycle resolves, capturing `api` as
// undefined for the process's whole life. `client.ts` pushes its fetcher in
// here instead, so the dependency runs one way. The type-only import below
// is erased at compile time and closes nothing.
import type { ShareEncryption } from '../api/types'

/** The fetcher `client.ts` installs once it has picked the real or the mock
 *  backend. Unset until then, which is a programming error rather than a
 *  state to tolerate: see `encryptedShares`. */
let source: (() => Promise<ShareEncryption[]>) | null = null

/** Installs the backend this cache reads through. Called once, from
 *  `api/client.ts`, at module initialisation. */
export function setEncryptedSharesSource(fetch: () => Promise<ShareEncryption[]>): void {
  source = fetch
}

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
    // Rejecting rather than resolving empty: an unset source means the api
    // layer was never initialised, and answering "no share is encrypted" to
    // that question is how plaintext reaches an encrypted share.
    const fetch = source
    cache =
      fetch === null
        ? Promise.reject(new Error('the encrypted-share source is not installed'))
        : fetch()
    // Not cached: the next caller tries again rather than reusing a
    // permanently-rejected promise for a transient failure.
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
 *  share label every destination and every listed entry is addressed by.
 *  Same rule `api/http.ts`'s `recentList` already splits a served path on. */
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
