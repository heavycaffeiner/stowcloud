// Messages between the page and the upload worker.
export interface AddItem {
  id: string
  file: File
  dest: string
  relativePath?: string
  /** True when the main thread prepared a whole ciphertext File. */
  encrypted?: boolean
  /** Account and browser-session context used to partition resume records. */
  accountId?: string
  sessionContext?: string
  /** Original source metadata and digests, before the worker sees ciphertext. */
  sourceName?: string
  sourceSize?: number
  sourceLastModified?: number
  sourceIdentity?: string
  /** Digest of the exact bytes in `file`, including encrypted ciphertext. */
  ciphertextIdentity?: string
}

export type Cmd =
  | { t: 'add'; items: AddItem[] }
  | { t: 'pause'; id: string }
  | { t: 'resume'; id: string }
  | { t: 'cancel'; id: string }
  | { t: 'csrf'; token: string }
  // The server's configured chunk floor/default (GET /api/auth/session's
  // `limits`), so a *new* session's starting chunk size actually reflects
  // an admin's `[upload]` config instead of this file's own hardcoded
  // constants. Same separate-module-realm reason `csrf` exists as a Cmd.
  | { t: 'limits'; chunkMin: number; chunkDefault: number }
  | { t: 'chunk-size'; size: number | null }
  | { t: 'concurrency'; maxInflight: number }
  /** Capability comes from the authenticated session response. */
  | { t: 'direct-capability'; supported: boolean }
export type Evt =
  | { t: 'progress'; id: string; sent: number; total: number; rate: number; etaSec: number }
  | { t: 'done'; id: string; dest: string; name: string; size: number; mtimeNs: string }
  | { t: 'error'; id: string; code: string; message: string; retryIn?: number }
  | { t: 'chunk-size-adjusted'; id: string; size: number }
  | { t: 'queued'; id: string; name: string; dest: string; total: number }
  | { t: 'canceled'; id: string }
  /** The worker no longer retains the prepared File for this item. */
  | { t: 'released'; id: string }
