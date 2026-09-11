// Dedicated upload Worker.
// Slicing + hashing on the main thread would jank the
// virtual-scrolled table, so every byte-pushing step happens here instead.
import {
  CHUNK_SIZE_DEFAULT,
  CHUNK_SIZE_MIN,
  CHUNK_SIZE_STORAGE_KEY,
  ChunkScheduler,
  loadStoredConcurrency,
  MAX_CONCURRENCY,
  MIN_CONCURRENCY,
  shrinkChunkSize,
  type ChunkDescriptor
} from './chunk-planner'
import {
  cleanupKey,
  deleteCleanupRecord,
  deleteResumeRecord,
  getCleanupRecords,
  getResumeRecord,
  putCleanupRecord,
  putResumeRecord,
  resumeKey,
  type CleanupRecord,
  type ResumeKeyContext,
  type ResumeRecord
} from './idb'
import { setCsrfToken, UploadHttpError, transport, type CreatedSession } from './transport'
import { classifyFailure } from './retry'

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
  | { t: 'concurrency'; maxInflight: number }
export type Evt =
  | { t: 'progress'; id: string; sent: number; total: number; rate: number; etaSec: number }
  | { t: 'done'; id: string; dest: string; name: string; size: number; mtimeNs: string }
  | { t: 'error'; id: string; code: string; message: string; retryIn?: number }
  | { t: 'chunk-size-adjusted'; id: string; size: number }
  | { t: 'queued'; id: string; name: string; dest: string; total: number }
  | { t: 'canceled'; id: string }
  /** The worker no longer retains the prepared File for this item. */
  | { t: 'released'; id: string }

interface FileState {
  id: string
  file: File
  dest: string
  relativePath?: string
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
  /** A failed DELETE leaves this state available for a later cleanup retry. */
  cleanupInFlight?: Promise<boolean>
  preparedReleased: boolean
}

function updateSentBytes(f: FileState): void {
  let inflight = 0
  for (const b of f.chunkProgress.values()) inflight += b
  f.sentBytes = Math.min(f.file.size, f.planStartOffset + f.acceptedBytes + inflight)
}

const files = new Map<string, FileState>()
let maxInflight = loadStoredConcurrency()
const scheduler = new ChunkScheduler(maxInflight)
let inflightRequests = 0

// A pause/cancel can arrive before addFile reaches its first post-await state.
// Keep the intent until that asynchronous setup installs the FileState.
const pendingControls = new Map<string, 'paused' | 'canceled'>()

/** In-memory copies keep an uncertain session recoverable for this worker's
 * lifetime even when IndexedDB is temporarily unavailable. The persisted copy
 * is still written whenever the browser storage allows it. */
const pendingCleanupRecords = new Map<string, CleanupRecord>()
const cleanupInFlight = new Map<string, Promise<boolean>>()
let cleanupRecoveryPromise: Promise<void> | null = null

// Server-reported floor/default, updated by the `limits` Cmd. Start from
// this file's own constants so a file added before the first `limits`
// message (or against an older server that doesn't send one) still works.
let serverChunkMin = CHUNK_SIZE_MIN
let serverChunkDefault = CHUNK_SIZE_DEFAULT


function releasePreparedItem(item: AddItem, id: string): void {
  if (item.encrypted) post({ t: 'released', id })
}

function releasePreparedFile(f: FileState): void {
  if (f.preparedReleased) return
  f.preparedReleased = true
  if (f.encrypted) post({ t: 'released', id: f.id })
}
function post(evt: Evt): void {
  ;(self as unknown as { postMessage(m: unknown): void }).postMessage(evt)
}

function loadStoredChunkSize(): number {
  try {
    const v = self.localStorage?.getItem(CHUNK_SIZE_STORAGE_KEY)
    return v ? Number(v) : serverChunkDefault
  } catch {
    return serverChunkDefault
  }
}

function storeChunkSize(size: number): void {
  try {
    self.localStorage?.setItem(CHUNK_SIZE_STORAGE_KEY, String(size))
  } catch {
    /* ignore (e.g. no localStorage in this worker context) */
  }
}

let creatingSessions = 0
const pendingSessionCreations: (() => void)[] = []

async function acquireSessionSlot(): Promise<void> {
  if (creatingSessions < 4) {
    creatingSessions++
    return
  }
  return new Promise<void>((resolve) => {
    pendingSessionCreations.push(resolve)
  })
}

function releaseSessionSlot(): void {
  const next = pendingSessionCreations.shift()
  if (next) {
    next()
  } else {
    creatingSessions--
  }
}

function sourceDetails(item: AddItem): { name: string; size: number; lastModified: number } {
  return {
    name: item.sourceName ?? item.file.name,
    size: item.sourceSize ?? item.file.size,
    lastModified: item.sourceLastModified ?? item.file.lastModified
  }
}

function resumeContextOf(item: AddItem): ResumeKeyContext | undefined {
  if (!item.accountId || !item.sessionContext || !item.sourceIdentity || !item.ciphertextIdentity) return undefined
  return {
    accountId: item.accountId,
    sessionContext: item.sessionContext,
    dest: item.dest,
    relativePath: item.relativePath
  }
}

function resumeRecordMatches(record: ResumeRecord, item: AddItem, key: string): boolean {
  const source = sourceDetails(item)
  return (
    record.key === key &&
    record.accountId === item.accountId &&
    record.sessionContext === item.sessionContext &&
    record.dest === item.dest &&
    record.relativePath === (item.relativePath ?? '') &&
    record.sourceName === source.name &&
    record.sourceSize === source.size &&
    record.sourceLastModified === source.lastModified &&
    record.sourceIdentity === item.sourceIdentity &&
    record.ciphertextIdentity === item.ciphertextIdentity
  )
}

function statusOf(err: unknown): number {
  if (err instanceof UploadHttpError) return err.status
  if (typeof err === 'object' && err !== null && 'status' in err && typeof err.status === 'number') {
    return err.status
  }
  return 0
}

function validChunkSize(size: number): boolean {
  return Number.isInteger(size) && size > 0
}

function validOffset(offset: number, total: number): boolean {
  return Number.isSafeInteger(offset) && offset >= 0 && offset <= total
}

function postStartError(id: string, err: unknown): void {
  const status = statusOf(err)
  const isQuota = status === 507
  const isDenied = status === 403
  post({
    t: 'error',
    id,
    code: isQuota ? 'upload.quota_exceeded' : isDenied ? 'upload.denied' : 'upload.failed',
    message: isQuota
      ? /* i18n */ 'upload.not_enough_storage_space_start'
      : isDenied
        ? /* i18n */ 'error.acl_denied'
        : /* i18n */ 'upload.could_not_start_upload'
  })
}

const CLEANUP_PENDING_CODE = /* i18n */ 'upload.cleanup_pending'
const CLEANUP_PENDING_MESSAGE = /* i18n */ 'upload.cleanup_pending'

function postCleanupPending(id: string): void {
  post({ t: 'error', id, code: CLEANUP_PENDING_CODE, message: CLEANUP_PENDING_MESSAGE })
}

function statusConfirmsRemoval(err: unknown): boolean {
  const status = statusOf(err)
  return status === 404 || status === 410
}

async function deleteSessionConfirmed(sessionId: string): Promise<boolean> {
  try {
    await transport.deleteSession(sessionId)
    return true
  } catch (err) {
    // A session that is already gone is the desired cancellation outcome,
    // including transports that surface 404/410 as rejected promises.
    return statusConfirmsRemoval(err)
  }
}

function cleanupRecordFor(sessionId: string): CleanupRecord {
  const existing = pendingCleanupRecords.get(sessionId)
  if (existing) return existing
  const record = { key: cleanupKey(sessionId), sessionId, updatedAt: Date.now() }
  pendingCleanupRecords.set(sessionId, record)
  return record
}

async function rememberCleanup(sessionId: string): Promise<CleanupRecord> {
  const record = cleanupRecordFor(sessionId)
  await putCleanupRecord(record).catch(() => {})
  return record
}

async function forgetCleanup(sessionId: string): Promise<void> {
  const record = pendingCleanupRecords.get(sessionId)
  pendingCleanupRecords.delete(sessionId)
  await deleteCleanupRecord(record?.key ?? cleanupKey(sessionId)).catch(() => {})
}

/** DELETE is confirmation of cancellation only when it resolves or explicitly
 * reports that the session is gone. An uncertain request leaves both the
 * resumable record (when present) and the independent cleanup record intact. */
async function cleanupServerSession(record: ResumeRecord): Promise<boolean> {
  return cleanupSession(record.sessionId, record)
}

async function cleanupCreatedSession(sessionId: string, record?: ResumeRecord): Promise<boolean> {
  if (!sessionId) return true
  return cleanupSession(sessionId, record)
}

function cleanupSession(sessionId: string, record?: ResumeRecord): Promise<boolean> {
  if (!sessionId) return Promise.resolve(true)
  const running = cleanupInFlight.get(sessionId)
  if (running) return running

  const run = (async (): Promise<boolean> => {
    try {
      await rememberCleanup(sessionId)
      if (record) await putResumeRecord({ ...record, cleanupPending: true }).catch(() => {})
      if (!(await deleteSessionConfirmed(sessionId))) return false
      if (record) await deleteResumeRecord(record.key).catch(() => {})
      await forgetCleanup(sessionId)
      return true
    } catch {
      // Cleanup failures are represented by the pending result. The session id
      // remains in memory and in IndexedDB whenever storage is available.
      return false
    }
  })()
  cleanupInFlight.set(sessionId, run)
  void run.finally(() => {
    if (cleanupInFlight.get(sessionId) === run) cleanupInFlight.delete(sessionId)
  })
  return run
}

/** Retry cleanup records retained by an earlier worker instance. A recovery
 * attempt is deliberately serial so a burst of stale sessions cannot turn
 * into another request storm. */
async function recoverPendingCleanups(): Promise<void> {
  const persisted = await getCleanupRecords().catch(() => [])
  const records = new Map<string, CleanupRecord>()
  for (const record of pendingCleanupRecords.values()) records.set(record.sessionId, record)
  for (const record of persisted) records.set(record.sessionId, record)
  for (const record of records.values()) await cleanupSession(record.sessionId)
}

function ensureCleanupRecovery(): Promise<void> {
  if (cleanupRecoveryPromise) return cleanupRecoveryPromise
  const run = recoverPendingCleanups().catch(() => {})
  cleanupRecoveryPromise = run
  void run.finally(() => {
    if (cleanupRecoveryPromise === run) cleanupRecoveryPromise = null
  })
  return run
}


function recordFor(item: AddItem, key: string, sessionId: string, chunkSize: number): ResumeRecord | undefined {
  const context = resumeContextOf(item)
  if (!context || !item.sourceIdentity || !item.ciphertextIdentity) return undefined
  const source = sourceDetails(item)
  return {
    key,
    sessionId,
    accountId: context.accountId,
    sessionContext: context.sessionContext,
    dest: context.dest,
    relativePath: context.relativePath ?? '',
    sourceName: source.name,
    sourceSize: source.size,
    sourceLastModified: source.lastModified,
    sourceIdentity: item.sourceIdentity,
    ciphertextIdentity: item.ciphertextIdentity,
    chunkSize,
    totalSize: item.file.size,
    updatedAt: Date.now()
  }
}

async function addFile(item: AddItem): Promise<void> {
  const id = item.id || `f-${Math.random().toString(36).slice(2, 10)}`
  // Posted before any await so a setup failure has a tray row to attach to.
  post({ t: 'queued', id, name: item.file.name, dest: item.dest, total: item.file.size })
  let handedOff = false

  try {
    if (pendingControls.get(id) === 'canceled') {
      pendingControls.delete(id)
      post({ t: 'canceled', id })
      return
    }

    const context = resumeContextOf(item)
    const source = sourceDetails(item)
    const key = context
      ? resumeKey(source.name, source.size, source.lastModified, context)
      : undefined

    await acquireSessionSlot()
    let sessionId = ''
    let resumeOffset = 0
    let chunkSize = loadStoredChunkSize()
    if (!validChunkSize(chunkSize)) chunkSize = CHUNK_SIZE_DEFAULT
    let resumeRecord: ResumeRecord | undefined
    try {
      if (pendingControls.get(id) === 'canceled') {
        pendingControls.delete(id)
        post({ t: 'canceled', id })
        return
      }

      const existing = key ? await getResumeRecord(key).catch(() => undefined) : undefined
      if (existing) {
        if (existing.cleanupPending) {
          if (!(await cleanupServerSession(existing))) {
            postCleanupPending(id)
            return
          }
        } else if (key === undefined || !resumeRecordMatches(existing, item, key)) {
          // Same metadata is not enough. Remove the old session only after the
          // server confirms cancellation, otherwise retain its identity.
          if (!(await cleanupServerSession(existing))) {
            postCleanupPending(id)
            return
          }
        } else {
          try {
            const head = await transport.headSession(existing.sessionId)
            const resumedChunkSize = head.chunkSize ?? existing.chunkSize
            if (
              head.totalSize !== item.file.size ||
              !validOffset(head.offset, item.file.size) ||
              !validChunkSize(resumedChunkSize)
            ) {
              if (!(await cleanupServerSession(existing))) {
                postCleanupPending(id)
                return
              }
            } else {
              sessionId = existing.sessionId
              resumeRecord = existing
              resumeOffset = head.offset
              chunkSize = resumedChunkSize
            }
          } catch (err) {
            const status = statusOf(err)
            if (status === 404 || status === 410) {
              // The server has confirmed that this old identity is gone.
              await deleteResumeRecord(existing.key).catch(() => {})
              await forgetCleanup(existing.sessionId)
            } else {
              // A timeout or authentication failure is not proof that the
              // session disappeared. Keep the record recoverable.
              postStartError(id, err)
              return
            }
          }
        }
      }

      if (!sessionId) {
        if (pendingControls.get(id) === 'canceled') {
          pendingControls.delete(id)
          post({ t: 'canceled', id })
          return
        }
        let created: CreatedSession
        try {
          created = await transport.createSession({
            filename: item.file.name,
            totalSize: item.file.size,
            chunkSize,
            dest: item.dest,
            relativePath: item.relativePath,
            mtimeNs: String(BigInt(item.file.lastModified) * 1_000_000n)
          })
        } catch (err) {
          postStartError(id, err)
          return
        }
        if (!created.id) {
          postStartError(id, new Error('upload session did not return an id'))
          return
        }
        sessionId = created.id
        resumeRecord = key ? recordFor(item, key, sessionId, chunkSize) : undefined
        if (!validOffset(created.offset, item.file.size)) {
          const cleaned = resumeRecord
            ? await cleanupServerSession(resumeRecord)
            : await cleanupCreatedSession(sessionId)
          if (!cleaned) postCleanupPending(id)
          else postStartError(id, new Error('upload session returned an invalid offset'))
          return
        }
        resumeOffset = created.offset
        if (resumeRecord) await putResumeRecord(resumeRecord).catch(() => {})

        if (pendingControls.get(id) === 'canceled') {
          pendingControls.delete(id)
          const cleaned = await cleanupCreatedSession(sessionId, resumeRecord)
          if (cleaned) post({ t: 'canceled', id })
          else postCleanupPending(id)
          return
        }
      }
    } catch (err) {
      if (pendingControls.get(id) === 'canceled') {
        pendingControls.delete(id)
        if (!sessionId) {
          post({ t: 'canceled', id })
        } else {
          const cleaned = await cleanupCreatedSession(sessionId, resumeRecord)
          if (cleaned) post({ t: 'canceled', id })
          else postCleanupPending(id)
        }
      } else {
        postStartError(id, err)
      }
      return
    } finally {
      releaseSessionSlot()
    }

    const intent = pendingControls.get(id)
    pendingControls.delete(id)
    if (intent === 'canceled') {
      const cleaned = await cleanupCreatedSession(sessionId, resumeRecord)
      if (cleaned) post({ t: 'canceled', id })
      else postCleanupPending(id)
      return
    }

    const f: FileState = {
      id,
      file: item.file,
      dest: item.dest,
      relativePath: item.relativePath,
      encrypted: item.encrypted === true,
      chunkSize,
      generation: 0,
      status: intent === 'paused' ? 'paused' : 'uploading',
      abort: new AbortController(),
      sessionId,
      resumeKey: resumeRecord?.key,
      resumeRecord,
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
    scheduler.addFile({ id, totalSize: item.file.size, chunkSize, resumeOffset, generation: f.generation })
    if (f.status === 'paused') scheduler.pause(id)
    handedOff = true
    if (f.status === 'uploading') {
      if (scheduler.isFileDone(id)) void finalizeIfDone(f)
      else pump()
    }
  } finally {
    if (!handedOff) releasePreparedItem(item, id)
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
  post({ t: 'error', id: f.id, code: 'upload.publication_uncertain', message: /* i18n */ 'upload.upload_failed_out_retries' })
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
      name: f.relativePath ? f.relativePath.split('/').pop()! : f.file.name,
      size: f.file.size,
      mtimeNs: String(BigInt(f.file.lastModified) * 1_000_000n)
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
    if (!current || current.generation !== task.generation || current.status === 'canceled' || current.status === 'error') {
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
        post({ t: 'error', id: f.id, code: 'upload.chunk_too_large', message: /* i18n */ 'upload.proxy_rejected_even_smallest_chunk' })
        releasePreparedFile(f)
      } else {
        f.chunkSize = next
        f.generation++
        storeChunkSize(next)
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
        scheduler.addFile({ id: f.id, totalSize: f.file.size, chunkSize: next, resumeOffset: f.baseSentBytes, generation: f.generation })
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
    post({ t: 'error', id: f.id, code: 'upload.retry', message: /* i18n */ 'upload.retrying', retryIn: verdict.afterMs })
    setTimeout(() => {
      const latest = files.get(f.id)
      if (!latest || latest.generation !== task.generation || latest.status === 'canceled' || latest.status === 'error' || latest.status === 'done') return
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

async function cancelFile(f: FileState): Promise<boolean> {
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
  void cancelFile(f).then((cleaned) => {
    if (cleaned) post({ t: 'canceled', id: f.id })
    else postCleanupPending(f.id)
    releasePreparedFile(f)
  })
}

self.addEventListener('message', (ev: MessageEvent<Cmd>) => {
  const cmd = ev.data
  switch (cmd.t) {
    case 'csrf':
      setCsrfToken(cmd.token)
      break
    case 'limits':
      serverChunkMin = cmd.chunkMin
      serverChunkDefault = cmd.chunkDefault
      break
    case 'add':
      void ensureCleanupRecovery().then(() => {
        for (const item of cmd.items) void addFile(item)
      })
      break
    case 'concurrency':
      maxInflight = Math.max(MIN_CONCURRENCY, Math.min(MAX_CONCURRENCY, Math.round(cmd.maxInflight)))
      scheduler.setMaxInflight(maxInflight)
      pump()
      break
    case 'pause': {
      const f = files.get(cmd.id)
      if (f) {
        if (f.status === 'uploading') f.status = 'paused'
        scheduler.pause(cmd.id)
      } else if (pendingControls.get(cmd.id) !== 'canceled') {
        pendingControls.set(cmd.id, 'paused')
      }
      break
    }
    case 'resume': {
      const f = files.get(cmd.id)
      if (f) {
        if (f.status === 'paused') f.status = 'uploading'
        scheduler.resume(cmd.id)
        if (f.status === 'uploading' && scheduler.isFileDone(cmd.id)) void finalizeIfDone(f)
        pump()
      } else if (pendingControls.get(cmd.id) !== 'canceled') {
        pendingControls.delete(cmd.id)
      }
      break
    }
    case 'cancel': {
      const f = files.get(cmd.id)
      if (!f) {
        pendingControls.set(cmd.id, 'canceled')
        break
      }
      if (f.status === 'done' || f.status === 'canceled') {
        reportCancellation(f)
        break
      }
      f.status = 'canceled'
      // Stop chunks already on the wire before the session goes, so a
      // cancelled upload stops sending rather than running to completion.
      f.abort.abort()
      scheduler.removeFile(cmd.id)
      forgetChunkRetries(cmd.id)
      // Do not report cancellation until DELETE confirms removal. An
      // uncertain request reports cleanup-pending and retains the session id.
      reportCancellation(f)
      break
    }
  }
})
