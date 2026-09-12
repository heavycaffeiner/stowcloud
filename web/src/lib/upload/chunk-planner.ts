// Pure upload-chunk planning.
// /, §8.
//
// Deliberately does not use an IntervalSet: a client only ever resumes from
// a single contiguous prefix (the value HEAD returns, per: "HEAD always returns the 0-based contiguous run end"), so planning
// is just "list fixed-size chunks from resumeOffset to totalSize". No
// merge/overlap bookkeeping is needed on the client.

export const CHUNK_SIZE_MIN = 5 * 1024 * 1024 // 5 MB, server-enforced floor
export const CHUNK_SIZE_DEFAULT = 10 * 1024 * 1024 // 10 MB

// Browser preferences are persisted by the window and sent to the upload worker.
export const CHUNK_SIZE_STORAGE_KEY = 'sc.chunk_size'

export const UPLOAD_CONCURRENCY_STORAGE_KEY = 'sc.upload_concurrency'
export const DEFAULT_CONCURRENCY = 4
export const MIN_CONCURRENCY = 1
export const MAX_CONCURRENCY = 16

const preferenceListeners = new Set<() => void>()

export function subscribeUploadPreferences(listener: () => void): () => void {
  preferenceListeners.add(listener)
  const onStorage = (event: StorageEvent): void => {
    if (event.key === null || event.key === CHUNK_SIZE_STORAGE_KEY || event.key === UPLOAD_CONCURRENCY_STORAGE_KEY) listener()
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

export function validChunkSizeOverride(value: number, min = CHUNK_SIZE_MIN): boolean {
  return Number.isSafeInteger(value) && value >= Math.max(CHUNK_SIZE_MIN, min)
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

export interface ChunkDescriptor {
  index: number
  offset: number
  length: number
}

/** Splits [resumeOffset, totalSize) into fixed-size chunks (last one may be shorter). */
export function planChunkOffsets(
  totalSize: number,
  chunkSize: number,
  resumeOffset = 0
): ChunkDescriptor[] {
  if (chunkSize <= 0) throw new RangeError('chunkSize must be > 0')
  if (resumeOffset >= totalSize) return []

  const chunks: ChunkDescriptor[] = []
  let offset = resumeOffset
  let index = Math.floor(resumeOffset / chunkSize)
  while (offset < totalSize) {
    const length = Math.min(chunkSize, totalSize - offset)
    chunks.push({ index, offset, length })
    offset += length
    index += 1
  }
  return chunks
}

/** On upstream 413: halve, floor at CHUNK_SIZE_MIN. Returns null once no further shrink is possible. */
export function shrinkChunkSize(current: number, min = CHUNK_SIZE_MIN): number | null {
  const next = Math.max(min, Math.floor(current / 2))
  return next === current ? null : next
}

export interface FileTask {
  id: string
  totalSize: number
  chunkSize: number
  resumeOffset: number
  /** Ownership token for this plan. A replacement plan must use a new token
   * so callbacks from its predecessor cannot requeue or complete new work. */
  generation?: number
}

interface TrackedFile extends FileTask {
  queue: ChunkDescriptor[]
  cursor: number
  inflight: Set<number>
  paused: boolean
  generation: number
}

/**
 * Hands out chunk work across many concurrently-uploading files under a
 * single GLOBAL concurrency cap ("parallel by chunk,
 * not by file": one large file alone still gets full parallelism).
 * Round-robins across files so no single huge file starves the others.
 */
export class ChunkScheduler {
  #files = new Map<string, TrackedFile>()
  #order: string[] = [] // round-robin ring of file ids
  #ring = 0
  #maxInflight: number

  constructor(maxInflight = 4) {
    this.#maxInflight = maxInflight
  }

  get maxInflight(): number {
    return this.#maxInflight
  }
  setMaxInflight(maxInflight: number): void {
    this.#maxInflight = Math.max(1, maxInflight)
  }


  get totalInflight(): number {
    let n = 0
    for (const f of this.#files.values()) n += f.inflight.size
    return n
  }

  addFile(task: FileTask): void {
    const queue = planChunkOffsets(task.totalSize, task.chunkSize, task.resumeOffset)
    const generation = task.generation ?? 0
    if (this.#files.has(task.id)) this.#order = this.#order.filter((x) => x !== task.id)
    this.#files.set(task.id, { ...task, generation, queue, cursor: 0, inflight: new Set(), paused: false })
    this.#order.push(task.id)
  }

  removeFile(id: string): void {
    this.#files.delete(id)
    this.#order = this.#order.filter((x) => x !== id)
  }

  pause(id: string): void {
    const f = this.#files.get(id)
    if (f) f.paused = true
  }

  resume(id: string): void {
    const f = this.#files.get(id)
    if (f) f.paused = false
  }

  isFileDone(id: string): boolean {
    const f = this.#files.get(id)
    if (!f) return true
    return f.cursor >= f.queue.length && f.inflight.size === 0
  }

  /** Pulls the next task to send, respecting the global inflight cap. Null if nothing is available right now. */
  next(): (ChunkDescriptor & { fileId: string; generation: number }) | null {
    if (this.totalInflight >= this.#maxInflight) return null
    const n = this.#order.length
    if (n === 0) return null

    for (let i = 0; i < n; i++) {
      const idx = (this.#ring + i) % n
      const id = this.#order[idx]
      const f = this.#files.get(id)
      if (!f || f.paused) continue
      if (f.cursor < f.queue.length) {
        const chunk = f.queue[f.cursor]
        f.cursor += 1
        f.inflight.add(chunk.index)
        this.#ring = (idx + 1) % n
        return { ...chunk, fileId: id, generation: f.generation }
      }
    }
    return null
  }

  complete(fileId: string, chunkIndex: number, generation?: number): void {
    const f = this.#files.get(fileId)
    if (!f || (generation !== undefined && f.generation !== generation)) return
    f.inflight.delete(chunkIndex)
  }

  /** Retries a chunk (e.g. after a transient failure): re-queues it at the front. */
  requeue(fileId: string, chunk: ChunkDescriptor & { generation?: number }, generation = chunk.generation): void {
    const f = this.#files.get(fileId)
    if (!f || (generation !== undefined && f.generation !== generation)) return
    f.inflight.delete(chunk.index)
    f.queue.splice(f.cursor, 0, { index: chunk.index, offset: chunk.offset, length: chunk.length })
  }
}
