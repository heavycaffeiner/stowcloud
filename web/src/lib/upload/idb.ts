// Client-side resume index. Records are partitioned by the authenticated
// account/session context and destination, and carry source and ciphertext
// digests so metadata collisions cannot resume the wrong byte stream.
// Runs inside the upload Worker (IndexedDB is available there too).

const DB_NAME = 'sc-uploads'
const DB_VERSION = 3
const STORE = 'sessions'
const CLEANUP_STORE = 'cleanup'

export interface ResumeKeyContext {
  accountId: string
  sessionContext: string
  dest: string
  relativePath?: string
}

export interface ResumeRecord {
  key: string
  sessionId: string
  accountId: string
  /** A non-secret fingerprint of the browser session context. */
  sessionContext: string
  dest: string
  relativePath: string
  sourceName: string
  sourceSize: number
  sourceLastModified: number
  /** Digest of the source bytes before any encryption. */
  sourceIdentity: string
  /** Digest of the exact bytes sent to the server. */
  ciphertextIdentity: string
  chunkSize: number
  totalSize: number
  updatedAt: number
  /** Set before a best-effort server abort. It remains until DELETE is
   * confirmed, so an uncertain cancellation cannot lose its only session id. */
  cleanupPending?: boolean
}

/** A cancellation recovery record does not depend on the metadata and
 * identity required to resume an upload. It exists solely to retain a server
 * session id until its DELETE is confirmed. */
export interface CleanupRecord {
  key: string
  sessionId: string
  updatedAt: number
}

export function cleanupKey(sessionId: string): string {
  return `v1:${sessionId}`
}

/** Stable key for one source and transfer target. Content identities remain
 * fields on the record and are checked before adoption, so a same-metadata
 * file cannot resume a different byte stream. */
export function resumeKey(
  name: string,
  size: number,
  lastModified: number,
  context: ResumeKeyContext
): string {
  return `v2:${JSON.stringify([
    context.accountId,
    context.sessionContext,
    context.dest,
    context.relativePath ?? '',
    name,
    size,
    lastModified
  ])}`
}

let dbPromise: Promise<IDBDatabase> | null = null

function openDb(): Promise<IDBDatabase> {
  if (dbPromise) return dbPromise
  dbPromise = new Promise((resolve, reject) => {
    const req = indexedDB.open(DB_NAME, DB_VERSION)
    req.onupgradeneeded = () => {
      const db = req.result
      if (!db.objectStoreNames.contains(STORE)) {
        db.createObjectStore(STORE, { keyPath: 'key' })
      }
      if (!db.objectStoreNames.contains(CLEANUP_STORE)) {
        db.createObjectStore(CLEANUP_STORE, { keyPath: 'key' })
      }
    }
    req.onsuccess = () => resolve(req.result)
    req.onerror = () => reject(req.error)
  })
  return dbPromise
}

export async function getResumeRecord(key: string): Promise<ResumeRecord | undefined> {
  const db = await openDb()
  return new Promise((resolve, reject) => {
    const tx = db.transaction(STORE, 'readonly')
    const req = tx.objectStore(STORE).get(key)
    req.onsuccess = () => resolve(req.result as ResumeRecord | undefined)
    req.onerror = () => reject(req.error)
  })
}

export async function putResumeRecord(rec: ResumeRecord): Promise<void> {
  const db = await openDb()
  return new Promise((resolve, reject) => {
    const tx = db.transaction(STORE, 'readwrite')
    tx.objectStore(STORE).put(rec)
    tx.oncomplete = () => resolve()
    tx.onerror = () => reject(tx.error)
  })
}

export async function deleteResumeRecord(key: string): Promise<void> {
  const db = await openDb()
  return new Promise((resolve, reject) => {
    const tx = db.transaction(STORE, 'readwrite')
    tx.objectStore(STORE).delete(key)
    tx.oncomplete = () => resolve()
    tx.onerror = () => reject(tx.error)
  })
}

export async function getCleanupRecords(): Promise<CleanupRecord[]> {
  const db = await openDb()
  const { promise, resolve, reject } = Promise.withResolvers<CleanupRecord[]>()
  const tx = db.transaction(CLEANUP_STORE, 'readonly')
  const req = tx.objectStore(CLEANUP_STORE).getAll()
  req.onsuccess = () => resolve(req.result as CleanupRecord[])
  req.onerror = () => reject(req.error)
  return promise
}

export async function putCleanupRecord(rec: CleanupRecord): Promise<void> {
  const db = await openDb()
  const { promise, resolve, reject } = Promise.withResolvers<void>()
  const tx = db.transaction(CLEANUP_STORE, 'readwrite')
  tx.objectStore(CLEANUP_STORE).put(rec)
  tx.oncomplete = () => resolve()
  tx.onerror = () => reject(tx.error)
  return promise
}

export async function deleteCleanupRecord(key: string): Promise<void> {
  const db = await openDb()
  const { promise, resolve, reject } = Promise.withResolvers<void>()
  const tx = db.transaction(CLEANUP_STORE, 'readwrite')
  tx.objectStore(CLEANUP_STORE).delete(key)
  tx.oncomplete = () => resolve()
  tx.onerror = () => reject(tx.error)
  return promise
}
