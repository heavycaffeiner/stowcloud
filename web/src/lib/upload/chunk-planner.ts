// web/src/lib/upload/chunk-planner.ts — pure upload-chunk planning.
// /, §8.
//
// Deliberately does not use an IntervalSet: a client only ever resumes from
// a single contiguous prefix (the value HEAD returns, per — "HEAD always returns the 0-based contiguous run end"), so planning
// is just "list fixed-size chunks from resumeOffset to totalSize". No
// merge/overlap bookkeeping is needed on the client.

export const CHUNK_SIZE_MIN = 5 * 1024 * 1024 // 5 MB, server-enforced floor
export const CHUNK_SIZE_DEFAULT = 10 * 1024 * 1024 // 10 MB

// The client-side chunk-size override (worker.ts's 413 shrink-adaptation
// persists here; the admin upload-settings section reads/writes the same
// key so both agree on what "the next session" starts from).
export const CHUNK_SIZE_STORAGE_KEY = 'sc.chunk_size'

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
}

interface TrackedFile extends FileTask {
  queue: ChunkDescriptor[]
  cursor: number
  inflight: Set<number>
  paused: boolean
}

/**
 * Hands out chunk work across many concurrently-uploading files under a
 * single GLOBAL concurrency cap ("parallel by chunk,
 * not by file" — one large file alone still gets full parallelism).
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

  get totalInflight(): number {
    let n = 0
    for (const f of this.#files.values()) n += f.inflight.size
    return n
  }

  addFile(task: FileTask): void {
    const queue = planChunkOffsets(task.totalSize, task.chunkSize, task.resumeOffset)
    this.#files.set(task.id, { ...task, queue, cursor: 0, inflight: new Set(), paused: false })
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
  next(): (ChunkDescriptor & { fileId: string }) | null {
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
        return { ...chunk, fileId: id }
      }
    }
    return null
  }

  complete(fileId: string, chunkIndex: number): void {
    this.#files.get(fileId)?.inflight.delete(chunkIndex)
  }

  /** Retries a chunk (e.g. after a transient failure): re-queues it at the front. */
  requeue(fileId: string, chunk: ChunkDescriptor): void {
    const f = this.#files.get(fileId)
    if (!f) return
    f.inflight.delete(chunk.index)
    f.queue.splice(f.cursor, 0, chunk)
  }
}
