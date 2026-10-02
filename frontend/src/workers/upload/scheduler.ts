// Sends the chunks of every file with an open session under one global cap, and owns each file from its
// first chunk to publication, failure or cancellation. A refused chunk is retried, or re-planned smaller.
import {
  CHUNK_SIZE_DEFAULT,
  CHUNK_SIZE_MIN,
  ChunkScheduler,
  DEFAULT_CONCURRENCY,
  MAX_CONCURRENCY,
  MIN_CONCURRENCY,
  shrinkChunkSize,
  validChunkSizeOverride,
  type ChunkDescriptor
} from '../../lib/upload/chunk-planner'
import { classifyFailure } from '../../lib/upload/retry'
import { cleanupCreatedSession, postCleanupPending, rememberCleanup } from './cleanup'
import { deleteResumeRecord, putResumeRecord, type ResumeRecord } from './persistence'
import { post } from './protocol'
import { statusOf, transport } from './transport'

const PROGRESS_HZ_MS = 100 // at most 10 Hz

/**
 * How many times each chunk has been retried, keyed by file, plan generation
 * and chunk index. A new chunk-size plan therefore owns a new retry budget.
 */
const chunkRetries = new Map<string, number>()

function forgetChunkRetries(fileId: string): void {
  const prefix = `${fileId}:`
  for (const key of chunkRetries.keys()) {
    if (key.startsWith(prefix)) chunkRetries.delete(key)
  }
}

interface FileState {
  id: string
  file: File
  dest: string
  encrypted: boolean
  chunkSize: number
  /** Every replacement plan increments this token. */
  generation: number
  status: 'uploading' | 'paused' | 'done' | 'error' | 'canceled'
  /** Aborts every chunk of this file that is still in flight. */
  abort: AbortController
  sessionId: string
  resumeKey?: string
  resumeRecord?: ResumeRecord
  /** Server-confirmed contiguous prefix, safe as the next plan's start. */
  baseSentBytes: number
  /** Fixed start and successful task bytes for the current generation. */
  planStartOffset: number
  acceptedBytes: number
  chunkProgress: Map<number, number>
  sentBytes: number
  lastPostAt: number
  lastPostBytes: number
  rate: number
  /** True only after a successful terminal PATCH response. */
  publicationConfirmed: boolean
  /** Prevent duplicate zero-byte publication requests. */
  publicationInFlight?: Promise<boolean>
  /** The cancellation in progress, shared by repeated cancel commands. */
  cleanupInFlight?: Promise<boolean>
  preparedReleased: boolean
}

function updateSentBytes(f: FileState): void {
  let inflight = 0
  for (const b of f.chunkProgress.values()) inflight += b
  f.sentBytes = Math.min(f.file.size, f.planStartOffset + f.acceptedBytes + inflight)
}

const files = new Map<string, FileState>()
let maxInflight = DEFAULT_CONCURRENCY
const scheduler = new ChunkScheduler(maxInflight)
let inflightRequests = 0

// Server-reported floor/default, updated by the `limits` Cmd. Start from
// the planner's defaults so a file added before the first `limits`
// message (or against an older server that doesn't send one) still works.
let serverChunkMin = CHUNK_SIZE_MIN
let serverChunkDefault = CHUNK_SIZE_DEFAULT
let chunkSizeOverride: number | null = null

export function setChunkLimits(chunkMin: number, chunkDefault: number): void {
  if (validChunkSizeOverride(chunkMin)) serverChunkMin = chunkMin
  if (validChunkSizeOverride(chunkDefault, serverChunkMin)) serverChunkDefault = chunkDefault
  else serverChunkDefault = Math.max(serverChunkDefault, serverChunkMin)
}

export function setChunkSizeOverride(size: number | null): void {
  chunkSizeOverride = size !== null && validChunkSizeOverride(size, serverChunkMin) ? size : null
}

/** The chunk size a new session asks for. A shrink after a proxy refusal also lowers it for later files. */
export function startingChunkSize(): number {
  return chunkSizeOverride !== null && validChunkSizeOverride(chunkSizeOverride, serverChunkMin)
    ? chunkSizeOverride
    : serverChunkDefault
}

export function setConcurrency(value: number): void {
  if (!Number.isFinite(value)) return
  maxInflight = Math.max(MIN_CONCURRENCY, Math.min(MAX_CONCURRENCY, Math.round(value)))
  scheduler.setMaxInflight(maxInflight)
  pump()
}

function releasePreparedFile(f: FileState): void {
  if (f.preparedReleased) return
  f.preparedReleased = true
  if (f.encrypted) post({ t: 'released', id: f.id })
}

export interface StartFile {
  id: string
  file: File
  dest: string
  encrypted: boolean
  chunkSize: number
  sessionId: string
  resumeRecord?: ResumeRecord
  resumeOffset: number
  paused: boolean
}

/** Takes over a file whose session is open. From here the scheduler owns its progress and its outcome. */
export function startFile(start: StartFile): void {
  const { id, file, chunkSize, resumeOffset } = start
  const f: FileState = {
    id,
    file,
    dest: start.dest,
    encrypted: start.encrypted,
    chunkSize,
    generation: 0,
    status: start.paused ? 'paused' : 'uploading',
    abort: new AbortController(),
    sessionId: start.sessionId,
    resumeKey: start.resumeRecord?.key,
    resumeRecord: start.resumeRecord,
    baseSentBytes: resumeOffset,
    planStartOffset: resumeOffset,
    acceptedBytes: 0,
    chunkProgress: new Map(),
    sentBytes: resumeOffset,
    lastPostAt: 0,
    lastPostBytes: resumeOffset,
    rate: 0,
    publicationConfirmed: false,
    preparedReleased: false
  }
  files.set(id, f)
  scheduler.addFile({ id, totalSize: file.size, chunkSize, resumeOffset, generation: f.generation })
  if (f.status === 'paused') scheduler.pause(id)
  if (f.status === 'uploading') {
    if (scheduler.isFileDone(id)) void finalizeIfDone(f)
    else pump()
  }
}

function maybePostProgress(f: FileState, force = false): void {
  const now = Date.now()
  if (!force && now - f.lastPostAt < PROGRESS_HZ_MS) return
  const dtSec = (now - f.lastPostAt) / 1000
  const instRate = dtSec > 0 ? (f.sentBytes - f.lastPostBytes) / dtSec : f.rate
  f.rate = f.rate === 0 ? instRate : f.rate * 0.7 + instRate * 0.3
  f.lastPostAt = now
  f.lastPostBytes = f.sentBytes
  const remaining = f.file.size - f.sentBytes
  const etaSec = f.rate > 0 ? remaining / f.rate : Number.POSITIVE_INFINITY
  post({ t: 'progress', id: f.id, sent: f.sentBytes, total: f.file.size, rate: f.rate, etaSec })
}

async function discardErroredFile(f: FileState): Promise<void> {
  if (f.resumeRecord) await putResumeRecord(f.resumeRecord).catch(() => {})
  else await rememberCleanup(f.sessionId)
  scheduler.removeFile(f.id)
  files.delete(f.id)
}

async function publicationError(f: FileState, err: unknown): Promise<void> {
  if (f.status === 'canceled' || (err instanceof DOMException && err.name === 'AbortError')) return
  f.status = 'error'
  await discardErroredFile(f)
  post({
    t: 'error',
    id: f.id,
    code: 'upload.publication_uncertain',
    message: /* i18n */ 'upload.upload_failed_out_retries'
  })
  releasePreparedFile(f)
}

/**
 * A successful terminal PATCH is the publication outcome. For an empty file,
 * and for a resumed session whose offset already equals the length, issue a
 * supported zero-byte PATCH so the server's normal final-byte publication path
 * runs. A failed HEAD is never treated as publication success.
 */
async function finalizeIfDone(f: FileState): Promise<boolean> {
  if (f.status !== 'uploading' || !scheduler.isFileDone(f.id)) return false
  if (f.publicationInFlight) return f.publicationInFlight

  const run = (async (): Promise<boolean> => {
    if (!f.publicationConfirmed) {
      try {
        const result = await transport.patchChunk(f.sessionId, f.file.size, new Blob(), f.abort.signal)
        const current = files.get(f.id)
        if (!current || current.generation !== f.generation || current.status !== 'uploading') return false
        if (result.offset < f.file.size) {
          await publicationError(f, new Error('publication response stopped before the declared length'))
          return false
        }
        f.publicationConfirmed = true
      } catch (err) {
        await publicationError(f, err)
        return false
      }
    }

    if (!f.publicationConfirmed || f.status !== 'uploading') return false
    f.status = 'done'
    f.baseSentBytes = f.file.size
    f.planStartOffset = f.file.size
    f.acceptedBytes = 0
    f.chunkProgress.clear()
    f.sentBytes = f.file.size
    maybePostProgress(f, true)
    if (f.resumeKey) await deleteResumeRecord(f.resumeKey).catch(() => {})
    post({
      t: 'done',
      id: f.id,
      dest: f.dest,
      size: f.file.size
    })
    forgetChunkRetries(f.id)
    scheduler.removeFile(f.id)
    files.delete(f.id)
    releasePreparedFile(f)
    return true
  })()
  f.publicationInFlight = run
  try {
    return await run
  } finally {
    if (f.publicationInFlight === run) f.publicationInFlight = undefined
  }
}

async function sendChunk(task: ChunkDescriptor & { fileId: string; generation: number }): Promise<void> {
  const f = files.get(task.fileId)
  if (!f || f.generation !== task.generation || f.status !== 'uploading') {
    scheduler.complete(task.fileId, task.index, task.generation)
    pump()
    return
  }

  inflightRequests++
  try {
    const blob = f.file.slice(task.offset, task.offset + task.length)
    const result = await transport.patchChunk(f.sessionId, task.offset, blob, f.abort.signal, (bytes) => {
      const cur = files.get(task.fileId)
      if (cur && cur.generation === task.generation && cur.status === 'uploading') {
        cur.chunkProgress.set(task.index, bytes)
        updateSentBytes(cur)
        maybePostProgress(cur)
      }
    })

    const current = files.get(task.fileId)
    if (
      !current ||
      current.generation !== task.generation ||
      current.status === 'canceled' ||
      current.status === 'error'
    ) {
      scheduler.complete(task.fileId, task.index, task.generation)
      return
    }
    f.chunkProgress.delete(task.index)
    f.acceptedBytes += task.length
    if (Number.isSafeInteger(result.offset) && result.offset >= 0) {
      f.baseSentBytes = Math.min(f.file.size, Math.max(f.baseSentBytes, result.offset))
    }
    updateSentBytes(f)
    chunkRetries.delete(`${task.fileId}:${task.generation}:${task.index}`)
    scheduler.complete(task.fileId, task.index, task.generation)
    if (result.offset >= f.file.size) f.publicationConfirmed = true
    maybePostProgress(f)
    await finalizeIfDone(f)
  } catch (err) {
    scheduler.complete(task.fileId, task.index, task.generation)
    const current = files.get(task.fileId)
    // A callback from an old chunk-size plan has no authority over the new
    // scheduler queue, progress, or retry budget.
    if (!current || current.generation !== task.generation || current.status !== 'uploading') return
    current.chunkProgress.delete(task.index)
    updateSentBytes(current)

    const status = statusOf(err)
    const key = `${task.fileId}:${task.generation}:${task.index}`
    const tries = chunkRetries.get(key) ?? 0
    const verdict = classifyFailure(status, tries)

    if (verdict.kind === 'shrink') {
      const next = shrinkChunkSize(f.chunkSize, serverChunkMin)
      if (next === null) {
        f.status = 'error'
        await discardErroredFile(f)
        post({
          t: 'error',
          id: f.id,
          code: 'upload.chunk_too_large',
          message: /* i18n */ 'upload.proxy_rejected_even_smallest_chunk'
        })
        releasePreparedFile(f)
      } else {
        f.chunkSize = next
        f.generation++
        chunkSizeOverride = next
        post({ t: 'chunk-size-adjusted', id: f.id, size: next })
        // The remaining bytes are re-planned at the smaller size. The server
        // response reports its contiguous prefix, unlike accepted byte totals:
        // an out-of-order final chunk cannot skip a missing earlier range.
        // Old tasks retain their generation and are ignored.
        forgetChunkRetries(f.id)
        f.chunkProgress.clear()
        f.planStartOffset = f.baseSentBytes
        f.acceptedBytes = 0
        f.sentBytes = f.baseSentBytes
        f.lastPostBytes = f.baseSentBytes
        f.lastPostAt = Date.now()
        f.rate = 0
        scheduler.removeFile(f.id)
        scheduler.addFile({
          id: f.id,
          totalSize: f.file.size,
          chunkSize: next,
          resumeOffset: f.baseSentBytes,
          generation: f.generation
        })
      }
      return
    }

    if (verdict.kind === 'give-up') {
      f.status = 'error'
      await discardErroredFile(f)
      forgetChunkRetries(f.id)
      const told =
        verdict.reason === 'session-gone'
          ? { code: 'upload.failed', message: /* i18n */ 'upload.session_gone' }
          : verdict.reason === 'quota'
            ? { code: 'upload.quota_exceeded', message: /* i18n */ 'upload.not_enough_storage_space_finish' }
            : verdict.reason === 'conflict'
              ? { code: 'upload.conflict', message: /* i18n */ 'upload.conflict' }
              : verdict.reason === 'denied'
                ? { code: 'upload.denied', message: /* i18n */ 'error.acl_denied' }
                : { code: 'upload.failed', message: /* i18n */ 'upload.upload_failed_out_retries' }
      post({ t: 'error', id: f.id, code: told.code, message: told.message })
      releasePreparedFile(f)
      return
    }

    chunkRetries.set(key, tries + 1)
    post({
      t: 'error',
      id: f.id,
      code: 'upload.retry',
      message: /* i18n */ 'upload.retrying',
      retryIn: verdict.afterMs
    })
    setTimeout(() => {
      const latest = files.get(f.id)
      if (
        !latest ||
        latest.generation !== task.generation ||
        latest.status === 'canceled' ||
        latest.status === 'error' ||
        latest.status === 'done'
      )
        return
      // Keep retry ownership in the scheduler while paused. Resume will
      // unpause it and pump the same failed chunk exactly once.
      scheduler.requeue(task.fileId, task, task.generation)
      if (latest.status === 'uploading') pump()
    }, verdict.afterMs)
  } finally {
    inflightRequests--
    pump()
  }
}

function pump(): void {
  while (inflightRequests < maxInflight) {
    const task = scheduler.next()
    if (!task) break
    void sendChunk(task)
  }
}

async function releaseSession(f: FileState): Promise<boolean> {
  if (f.cleanupInFlight) return f.cleanupInFlight
  const run = (async (): Promise<boolean> => {
    const cleaned = await cleanupCreatedSession(f.sessionId, f.resumeRecord)
    scheduler.removeFile(f.id)
    files.delete(f.id)
    return cleaned
  })()
  f.cleanupInFlight = run
  try {
    return await run
  } finally {
    if (f.cleanupInFlight === run) f.cleanupInFlight = undefined
  }
}

function reportCancellation(f: FileState): void {
  if (f.cleanupInFlight) return
  void releaseSession(f).then((cleaned) => {
    if (cleaned) post({ t: 'canceled', id: f.id })
    else postCleanupPending(f.id)
    releasePreparedFile(f)
  })
}

/** False when the scheduler does not hold the file yet. */
export function pauseFile(id: string): boolean {
  const f = files.get(id)
  if (!f) return false
  if (f.status === 'uploading') f.status = 'paused'
  scheduler.pause(id)
  return true
}

/** False when the scheduler does not hold the file yet. */
export function resumeFile(id: string): boolean {
  const f = files.get(id)
  if (!f) return false
  if (f.status === 'paused') f.status = 'uploading'
  scheduler.resume(id)
  if (f.status === 'uploading' && scheduler.isFileDone(id)) void finalizeIfDone(f)
  pump()
  return true
}

/** False when the scheduler does not hold the file yet. */
export function cancelFile(id: string): boolean {
  const f = files.get(id)
  if (!f) return false
  if (f.status === 'done' || f.status === 'canceled') {
    reportCancellation(f)
    return true
  }
  f.status = 'canceled'
  // Stop chunks already on the wire before the session goes, so a
  // cancelled upload stops sending rather than running to completion.
  f.abort.abort()
  scheduler.removeFile(id)
  forgetChunkRetries(id)
  // Do not report cancellation until DELETE confirms removal. An
  // uncertain request reports cleanup-pending and retains the session id.
  reportCancellation(f)
  return true
}
