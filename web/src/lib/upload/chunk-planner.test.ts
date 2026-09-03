import { describe, expect, it } from 'vitest'
import {
  CHUNK_SIZE_MIN,
  ChunkScheduler,
  planChunkOffsets,
  shrinkChunkSize
} from './chunk-planner'

describe('planChunkOffsets', () => {
  it('splits an exact multiple into equal chunks', () => {
    const chunks = planChunkOffsets(30, 10)
    expect(chunks).toEqual([
      { index: 0, offset: 0, length: 10 },
      { index: 1, offset: 10, length: 10 },
      { index: 2, offset: 20, length: 10 }
    ])
  })

  it('produces a shorter final chunk for a non-multiple size', () => {
    const chunks = planChunkOffsets(25, 10)
    expect(chunks.at(-1)).toEqual({ index: 2, offset: 20, length: 5 })
    expect(chunks).toHaveLength(3)
  })

  it('resumes from a non-zero offset, preserving the original chunk index', () => {
    const chunks = planChunkOffsets(30, 10, 10)
    expect(chunks).toEqual([
      { index: 1, offset: 10, length: 10 },
      { index: 2, offset: 20, length: 10 }
    ])
  })

  it('returns nothing when already fully uploaded', () => {
    expect(planChunkOffsets(30, 10, 30)).toEqual([])
  })

  it('rejects a zero or negative chunk size', () => {
    expect(() => planChunkOffsets(10, 0)).toThrow(RangeError)
  })
})

describe('shrinkChunkSize (upstream 413 handling)', () => {
  it('halves the chunk size', () => {
    expect(shrinkChunkSize(10 * 1024 * 1024)).toBe(5 * 1024 * 1024)
  })

  it('floors at CHUNK_SIZE_MIN (5 MB)', () => {
    expect(shrinkChunkSize(6 * 1024 * 1024)).toBe(CHUNK_SIZE_MIN)
  })

  it('returns null once no further shrink is possible (already at floor)', () => {
    expect(shrinkChunkSize(CHUNK_SIZE_MIN)).toBeNull()
  })
})

describe('ChunkScheduler', () => {
  it('never exceeds the global concurrency cap across multiple files', () => {
    const sched = new ChunkScheduler(4)
    sched.addFile({ id: 'a', totalSize: 1000, chunkSize: 10, resumeOffset: 0 })
    sched.addFile({ id: 'b', totalSize: 1000, chunkSize: 10, resumeOffset: 0 })

    const handed: unknown[] = []
    for (let i = 0; i < 10; i++) {
      const t = sched.next()
      if (t) handed.push(t)
    }
    expect(handed.length).toBe(4)
    expect(sched.totalInflight).toBe(4)
    expect(sched.next()).toBeNull() // cap reached
  })

  it('gives full parallelism to a single large file (chunk-level, not file-level)', () => {
    const sched = new ChunkScheduler(4)
    sched.addFile({ id: 'solo', totalSize: 1000, chunkSize: 10, resumeOffset: 0 })
    const handed = [sched.next(), sched.next(), sched.next(), sched.next()]
    expect(handed.every((t) => t?.fileId === 'solo')).toBe(true)
    expect(sched.totalInflight).toBe(4)
  })

  it('frees a slot on complete() so the next chunk can be handed out', () => {
    const sched = new ChunkScheduler(1)
    sched.addFile({ id: 'a', totalSize: 30, chunkSize: 10, resumeOffset: 0 })
    const first = sched.next()
    expect(first).not.toBeNull()
    expect(sched.next()).toBeNull() // cap of 1 reached
    sched.complete('a', first!.index)
    const second = sched.next()
    expect(second?.index).toBe(1)
  })

  it('reports a file done only once its queue is drained and nothing is inflight', () => {
    const sched = new ChunkScheduler(4)
    sched.addFile({ id: 'a', totalSize: 10, chunkSize: 10, resumeOffset: 0 })
    expect(sched.isFileDone('a')).toBe(false)
    const t = sched.next()!
    expect(sched.isFileDone('a')).toBe(false) // still inflight
    sched.complete('a', t.index)
    expect(sched.isFileDone('a')).toBe(true)
  })

  it('does not hand out chunks for a paused file', () => {
    const sched = new ChunkScheduler(4)
    sched.addFile({ id: 'a', totalSize: 30, chunkSize: 10, resumeOffset: 0 })
    sched.pause('a')
    expect(sched.next()).toBeNull()
    sched.resume('a')
    expect(sched.next()).not.toBeNull()
  })

  it('requeue puts a failed chunk back for retry ahead of unsent work', () => {
    const sched = new ChunkScheduler(4)
    sched.addFile({ id: 'a', totalSize: 20, chunkSize: 10, resumeOffset: 0 })
    const t = sched.next()!
    sched.requeue('a', t)
    const retried = sched.next()
    expect(retried?.index).toBe(t.index)
  })

  it('drains every file when multiple single-chunk files are scheduled', () => {
    const sched = new ChunkScheduler(4)
    sched.addFile({ id: 'f1', totalSize: 10, chunkSize: 10, resumeOffset: 0 })
    sched.addFile({ id: 'f2', totalSize: 10, chunkSize: 10, resumeOffset: 0 })
    sched.addFile({ id: 'f3', totalSize: 10, chunkSize: 10, resumeOffset: 0 })
    sched.addFile({ id: 'f4', totalSize: 10, chunkSize: 10, resumeOffset: 0 })
    sched.addFile({ id: 'f5', totalSize: 10, chunkSize: 10, resumeOffset: 0 })
    sched.addFile({ id: 'f6', totalSize: 10, chunkSize: 10, resumeOffset: 0 })

    const completed: string[] = []
    let inflight = 0
    function pump() {
      while (inflight < 4) {
        const task = sched.next()
        if (!task) break
        inflight++
        // Simulate chunk completion
        sched.complete(task.fileId, task.index)
        if (sched.isFileDone(task.fileId)) {
          completed.push(task.fileId)
          sched.removeFile(task.fileId)
        }
        inflight--
        pump()
      }
    }
    pump()
    expect(completed.sort()).toEqual(['f1', 'f2', 'f3', 'f4', 'f5', 'f6'])
  })
})
