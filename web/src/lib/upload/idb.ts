// web/src/lib/upload/idb.ts — client-side resume index.
// /: "Persist session ids in
// IndexedDB keyed by (name, size, lastModified) so a page reload resumes."
// Runs inside the upload Worker (IndexedDB is available there too).

const DB_NAME = 'sc-uploads'
const DB_VERSION = 1
const STORE = 'sessions'

export interface ResumeRecord {
  key: string
  sessionId: string
  dest: string
  chunkSize: number
  totalSize: number
  updatedAt: number
}

export function resumeKey(name: string, size: number, lastModified: number): string {
  return `${name}|${size}|${lastModified}`
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
