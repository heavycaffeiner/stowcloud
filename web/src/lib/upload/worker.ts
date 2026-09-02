// web/src/lib/upload/worker.ts — dedicated upload Worker.
// Slicing + hashing on the main thread would jank the
// virtual-scrolled table, so every byte-pushing step happens here instead.
import {
  CHUNK_SIZE_DEFAULT,
  CHUNK_SIZE_MIN,
  CHUNK_SIZE_STORAGE_KEY,
  ChunkScheduler,
  shrinkChunkSize,
  type ChunkDescriptor
} from './chunk-planner'
import { deleteResumeRecord, getResumeRecord, putResumeRecord, resumeKey } from './idb'
import { setCsrfToken, UploadHttpError, mockTransport, transport } from './transport'
import { classifyFailure } from './retry'

const IS_MOCK = import.meta.env.VITE_API_MOCK === '1'
const MAX_INFLIGHT = 4 // global, per-chunk, not per file
const PROGRESS_HZ_MS = 100 // at most 10 Hz

/**
 * How many times each chunk has been retried, keyed by file and chunk index.
 *
 * Per chunk because MAX_INFLIGHT of them are on the wire together: one dropped
 * connection fails every chunk in flight, and a single counter shared by the
 * file read that as one failure per chunk rather than as one outage.
 */
const chunkRetries = new Map<string, number>()

/** Drops every chunk's retry count for one file, which its keys are prefixed
 *  with. Called wherever a file leaves the map, so a long session does not
 *  accumulate counts for uploads that finished. */
function forgetChunkRetries(fileId: string): void {
  const prefix = `${fileId}:`
  for (const key of chunkRetries.keys()) {
    if (key.startsWith(prefix)) chunkRetries.delete(key)
  }
}

export interface AddItem {
  file: File
  dest: string
  relativePath?: string
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

export type Evt =
  | { t: 'progress'; id: string; sent: number; total: number; rate: number; etaSec: number }
  | { t: 'done'; id: string; dest: string; name: string; size: number; mtimeNs: string }
  | { t: 'error'; id: string; code: string; message: string; retryIn?: number }
  | { t: 'chunk-size-adjusted'; id: string; size: number }
  | { t: 'queued'; id: string; name: string; dest: string; total: number }
  | { t: 'canceled'; id: string }

interface FileState {
  id: string
  file: File
  dest: string
  relativePath?: string
  chunkSize: number
  status: 'uploading' | 'paused' | 'done' | 'error' | 'canceled'
  /** Aborts every chunk of this file that is still in flight. */
  abort: AbortController
  sessionId: string
  sentBytes: number
  lastPostAt: number
  lastPostBytes: number
  rate: number
}

const files = new Map<string, FileState>()
const scheduler = new ChunkScheduler(MAX_INFLIGHT)
let inflightRequests = 0

// Server-reported floor/default, updated by the `limits` Cmd. Start from
// this file's own constants so a file added before the first `limits`
// message (or against an older server that doesn't send one) still works.
let serverChunkMin = CHUNK_SIZE_MIN
let serverChunkDefault = CHUNK_SIZE_DEFAULT

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

async function addFile(item: AddItem): Promise<void> {
  const id = `f-${Math.random().toString(36).slice(2, 10)}`
  // Posted before any await so a `createSession` failure below has a tray
  // row to attach its error to — `UploadTrayState#patch` is a no-op against
  // an id it has never seen `queued` for, which would otherwise swallow the
  // failure silently instead of showing it.
  post({ t: 'queued', id, name: item.file.name, dest: item.dest, total: item.file.size })
  const key = resumeKey(item.file.name, item.file.size, item.file.lastModified)
  const existing = await getResumeRecord(key).catch(() => undefined)

  let sessionId: string
  let resumeOffset = 0
  let chunkSize = loadStoredChunkSize()

  if (existing) {
    try {
      const head = await transport.headSession(existing.sessionId)
      sessionId = existing.sessionId
      resumeOffset = head.offset
      // The session's chunk size is fixed server-side at creation
      // and can't change — trust `Sc-Chunk-Size` over
      // the IDB record, which can be stale (e.g. if a 413 shrink was
      // recorded locally after this session was already created, or the
      // server's config changed since). Falls back to the IDB value only
      // against an older server that doesn't send the header yet.
      chunkSize = head.chunkSize ?? existing.chunkSize
    } catch {
      // session expired/gone server-side — start fresh below
      sessionId = ''
    }
  } else {
    sessionId = ''
  }

  if (!sessionId) {
    let created: Awaited<ReturnType<typeof transport.createSession>>
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
      // Session creation has no retry loop of its own (unlike sendChunk) —
      // surface a terminal error instead of leaving this file silently stuck.
      const status = err instanceof UploadHttpError ? err.status : 0
      post({
        t: 'error',
        id,
        code: status === 507 ? 'upload.quota_exceeded' : 'upload.failed',
        // Catalogue keys, not display text: this worker has no locale state,
        // so `UploadTray.svelte` runs them through `t()` at the render site.
        message: status === 507 ? /* i18n */ 'upload.not_enough_storage_space_start' : /* i18n */ 'upload.could_not_start_upload'
      })
      return
    }
    sessionId = created.id
    resumeOffset = created.offset
    await putResumeRecord({
      key,
      sessionId,
      dest: item.dest,
      chunkSize,
      totalSize: item.file.size,
      updatedAt: Date.now()
    })
  }

  files.set(id, {
    id,
    file: item.file,
    dest: item.dest,
    relativePath: item.relativePath,
    chunkSize,
    status: 'uploading',
    abort: new AbortController(),
    sessionId,
    sentBytes: resumeOffset,
    lastPostAt: 0,
    lastPostBytes: resumeOffset,
    rate: 0
  })

  scheduler.addFile({ id, totalSize: item.file.size, chunkSize, resumeOffset })
  pump()
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

async function finalizeIfDone(f: FileState): Promise<boolean> {
  if (!scheduler.isFileDone(f.id)) return false
  const head = await transport.headSession(f.sessionId).catch(() => null)
  const complete = head ? head.offset >= f.file.size : IS_MOCK ? mockTransport.getSession(f.sessionId)?.received === f.file.size : true
  if (!complete) return false

  f.status = 'done'
  f.sentBytes = f.file.size
  maybePostProgress(f, true)
  await deleteResumeRecord(resumeKey(f.file.name, f.file.size, f.file.lastModified)).catch(() => {})
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
  return true
}

async function sendChunk(task: ChunkDescriptor & { fileId: string }): Promise<void> {
  const f = files.get(task.fileId)
  if (!f || f.status !== 'uploading') {
    scheduler.complete(task.fileId, task.index)
    return
  }

  inflightRequests++
  try {
    const blob = f.file.slice(task.offset, task.offset + task.length)
    await transport.patchChunk(f.sessionId, task.offset, blob, f.abort.signal)
    f.sentBytes = Math.min(f.file.size, f.sentBytes + task.length)
    chunkRetries.delete(`${task.fileId}:${task.index}`)
    scheduler.complete(task.fileId, task.index)
    maybePostProgress(f)
    const done = await finalizeIfDone(f)
    if (!done) pump()
  } catch (err) {
    scheduler.complete(task.fileId, task.index)

    // Cancel deletes the session, so every chunk still in flight fails right
    // after it. Those failures are the cancellation working, not a fault to
    // recover from: retrying them put the row back to "retrying" and left a
    // cancelled upload that could not be cancelled again, because the row it
    // belonged to was already gone from `files`.
    if (files.get(task.fileId)?.status !== 'uploading') return

    const status = err instanceof UploadHttpError ? err.status : 0
    // Counted per chunk, not per file. Four chunks are in flight at once and a
    // dropped connection fails all of them together, so a file-wide counter
    // spent its whole budget on one outage and gave up on the first.
    const key = `${task.fileId}:${task.index}`
    const tries = chunkRetries.get(key) ?? 0
    const verdict = classifyFailure(status, tries)

    if (verdict.kind === 'shrink') {
      const next = shrinkChunkSize(f.chunkSize, serverChunkMin)
      if (next === null) {
        f.status = 'error'
        post({ t: 'error', id: f.id, code: 'upload.chunk_too_large', message: /* i18n */ 'upload.proxy_rejected_even_smallest_chunk' })
      } else {
        f.chunkSize = next
        storeChunkSize(next)
        post({ t: 'chunk-size-adjusted', id: f.id, size: next })
        // The remaining bytes are re-planned at the smaller size, so the old
        // chunk indices no longer name anything.
        forgetChunkRetries(f.id)
        scheduler.removeFile(f.id)
        scheduler.addFile({ id: f.id, totalSize: f.file.size, chunkSize: next, resumeOffset: f.sentBytes })
        scheduler.requeue(f.id, task)
      }
      pump()
      return
    }

    if (verdict.kind === 'give-up') {
      f.status = 'error'
      forgetChunkRetries(f.id)
      const told =
        verdict.reason === 'session-gone'
          ? { code: 'upload.failed', message: /* i18n */ 'upload.session_gone' }
          : verdict.reason === 'quota'
            ? { code: 'upload.quota_exceeded', message: /* i18n */ 'upload.not_enough_storage_space_finish' }
            : { code: 'upload.failed', message: /* i18n */ 'upload.upload_failed_out_retries' }
      post({ t: 'error', id: f.id, code: told.code, message: told.message })
      pump()
      return
    }

    chunkRetries.set(key, tries + 1)
    post({ t: 'error', id: f.id, code: 'upload.retry', message: /* i18n */ 'upload.retrying', retryIn: verdict.afterMs })
    setTimeout(() => {
      if (files.get(f.id)?.status === 'uploading') {
        scheduler.requeue(task.fileId, task)
        pump()
      }
    }, verdict.afterMs)
  } finally {
    inflightRequests--
  }
}

function pump(): void {
  while (inflightRequests < MAX_INFLIGHT) {
    const task = scheduler.next()
    if (!task) break
    void sendChunk(task)
  }
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
      for (const item of cmd.items) void addFile(item)
      break
    case 'pause': {
      const f = files.get(cmd.id)
      if (f) f.status = 'paused'
      scheduler.pause(cmd.id)
      break
    }
    case 'resume': {
      const f = files.get(cmd.id)
      if (f) f.status = 'uploading'
      scheduler.resume(cmd.id)
      pump()
      break
    }
    case 'cancel': {
      const f = files.get(cmd.id)
      if (f) {
        f.status = 'canceled'
        // Stop the chunks already on the wire before the session goes, so a
        // cancelled upload stops sending rather than running to completion
        // and failing afterwards.
        f.abort.abort()
        void transport.deleteSession(f.sessionId).catch(() => {})
        void deleteResumeRecord(resumeKey(f.file.name, f.file.size, f.file.lastModified)).catch(() => {})
        forgetChunkRetries(cmd.id)
        scheduler.removeFile(cmd.id)
        files.delete(cmd.id)
        post({ t: 'canceled', id: cmd.id })
      }
      break
    }
  }
})
