// Upload preferences kept in the browser's storage. The page reads them and
// sends them to the upload worker, which has no storage of its own to read.
import {
  CHUNK_SIZE_MIN,
  DEFAULT_CONCURRENCY,
  MAX_CONCURRENCY,
  MIN_CONCURRENCY,
  validChunkSizeOverride
} from './chunk-planner'

export const CHUNK_SIZE_STORAGE_KEY = 'sc.chunk_size'

export const UPLOAD_CONCURRENCY_STORAGE_KEY = 'sc.upload_concurrency'

const preferenceListeners = new Set<() => void>()

export function subscribeUploadPreferences(listener: () => void): () => void {
  preferenceListeners.add(listener)
  const onStorage = (event: StorageEvent): void => {
    if (event.key === null || event.key === CHUNK_SIZE_STORAGE_KEY || event.key === UPLOAD_CONCURRENCY_STORAGE_KEY)
      listener()
  }
  if (typeof window !== 'undefined') window.addEventListener('storage', onStorage)
  return () => {
    preferenceListeners.delete(listener)
    if (typeof window !== 'undefined') window.removeEventListener('storage', onStorage)
  }
}

function notifyPreferences(): void {
  for (const listener of preferenceListeners) listener()
}

export function loadStoredChunkSize(min = CHUNK_SIZE_MIN): number | null {
  try {
    const raw = localStorage.getItem(CHUNK_SIZE_STORAGE_KEY)
    if (!raw) return null
    const value = Number(raw)
    return validChunkSizeOverride(value, min) ? value : null
  } catch {
    return null
  }
}

export function storeChunkSize(value: number | null, min = CHUNK_SIZE_MIN): boolean {
  if (value !== null && !validChunkSizeOverride(value, min)) return false
  try {
    if (value === null) localStorage.removeItem(CHUNK_SIZE_STORAGE_KEY)
    else localStorage.setItem(CHUNK_SIZE_STORAGE_KEY, String(value))
  } catch {
    return false
  }
  notifyPreferences()
  return true
}

export function loadStoredConcurrency(): number {
  try {
    const raw = typeof localStorage !== 'undefined' ? localStorage.getItem(UPLOAD_CONCURRENCY_STORAGE_KEY) : null
    if (!raw) return DEFAULT_CONCURRENCY
    const n = Number(raw)
    if (!Number.isFinite(n)) return DEFAULT_CONCURRENCY
    return Math.max(MIN_CONCURRENCY, Math.min(MAX_CONCURRENCY, Math.round(n)))
  } catch {
    return DEFAULT_CONCURRENCY
  }
}

export function storeConcurrency(value: number): boolean {
  if (!Number.isFinite(value)) return false
  try {
    const clamped = Math.max(MIN_CONCURRENCY, Math.min(MAX_CONCURRENCY, Math.round(value)))
    localStorage.setItem(UPLOAD_CONCURRENCY_STORAGE_KEY, String(clamped))
  } catch {
    return false
  }
  notifyPreferences()
  return true
}
