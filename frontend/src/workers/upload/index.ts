// Dedicated upload Worker, so slicing and hashing never jank the page. This entry takes the page's
// commands and opens or resumes a session for each added file, then hands it to the scheduler. When the
// server offers direct uploads, a file goes straight to object storage instead.
import {
  cleanupCreatedSession,
  cleanupServerSession,
  cleanupSession,
  ensureCleanupRecovery,
  forgetCleanup,
  postCleanupPending
} from './cleanup'
import {
  deleteResumeRecord,
  getResumeRecord,
  putResumeRecord,
  resumeKeyOf,
  resumeRecordFor,
  resumeRecordMatches,
  type ResumeRecord
} from './persistence'
import { post, type AddItem, type Cmd } from './protocol'
import {
  cancelFile,
  pauseFile,
  resumeFile,
  setChunkLimits,
  setChunkSizeOverride,
  setConcurrency,
  startFile,
  startingChunkSize
} from './scheduler'
import {
  setCsrfToken,
  statusOf,
  UploadHttpError,
  DirectUploadUnsupportedError,
  transport,
  type CreatedSession,
  type DirectPart,
  type DirectReservation
} from './transport'

// A pause/cancel can arrive before addFile reaches its first post-await state.
// Keep the intent until that asynchronous setup hands the file to the scheduler.
const pendingControls = new Map<string, 'paused' | 'canceled'>()
const directReservations = new Map<string, DirectReservation>()
const directAborts = new Map<string, AbortController>()
let directCapability = false

function releasePreparedItem(item: AddItem, id: string): void {
  if (item.encrypted) post({ t: 'released', id })
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

async function sha256Hex(blob: Blob): Promise<string> {
  const digest = await crypto.subtle.digest('SHA-256', await blob.arrayBuffer())
  return Array.from(new Uint8Array(digest), (byte) => byte.toString(16).padStart(2, '0')).join('')
}

async function cleanupDirectReservation(id: string, reservation: DirectReservation): Promise<boolean> {
  directAborts.get(id)?.abort()
  directAborts.delete(id)
  const cleaned = await cleanupSession(reservation.id, undefined, true)
  if (cleaned) directReservations.delete(id)
  return cleaned
}
async function cancelDirectReservation(id: string, reservation: DirectReservation): Promise<boolean> {
  const cleaned = await cleanupDirectReservation(id, reservation)
  if (cleaned) post({ t: 'canceled', id })
  else postCleanupPending(id)
  return cleaned
}

async function tryDirectUpload(item: AddItem, id: string): Promise<boolean> {
  if (item.file.size === 0) return false
  if (
    !directCapability ||
    !transport.reserveDirect ||
    !transport.directPart ||
    !transport.uploadDirectPart ||
    !transport.completeDirect
  )
    return false
  const path = `${item.dest}/${item.relativePath ?? item.file.name}`.replace(/\/{2,}/g, '/').replace(/^\/+/, '')
  let reservation: DirectReservation
  try {
    reservation = await transport.reserveDirect({
      path,
      size: item.file.size,
      ...(item.ciphertextIdentity ? { checksum: item.ciphertextIdentity } : {})
    })
  } catch (error) {
    if (error instanceof DirectUploadUnsupportedError) return false
    throw error
  }
  directReservations.set(id, reservation)
  const abort = new AbortController()
  directAborts.set(id, abort)
  if (reservation.size !== item.file.size || reservation.partSize <= 0)
    throw new UploadHttpError(502, 'direct upload reservation was inconsistent')
  if (item.file.size > reservation.partSize * 10000) {
    await cleanupSession(reservation.id, undefined, true)
    directReservations.delete(id)
    directAborts.delete(id)
    return false
  }
  const existing = new Map(reservation.parts.map((part) => [part.partNumber, part]))
  const completed: DirectPart[] = []
  const count = Math.ceil(item.file.size / reservation.partSize)
  for (let index = 0; index < count; index++) {
    const partNumber = index + 1
    const offset = index * reservation.partSize
    const size = Math.min(reservation.partSize, item.file.size - offset)
    const saved = existing.get(partNumber)
    if (saved && saved.size === size && saved.etag) {
      completed.push({ partNumber, size, etag: saved.etag, ...(saved.checksum ? { checksum: saved.checksum } : {}) })
    } else {
      if (pendingControls.get(id) === 'canceled') {
        pendingControls.delete(id)
        await cancelDirectReservation(id, reservation)
        return true
      }
      const body = item.file.slice(offset, offset + size)
      const checksum = await sha256Hex(body)
      const url = await transport.directPart(reservation.id, partNumber, size, checksum)
      const uploaded = await transport.uploadDirectPart(url, body, abort.signal)
      completed.push({ partNumber, size, etag: uploaded.etag, checksum })
      if (pendingControls.get(id) === 'canceled') {
        pendingControls.delete(id)
        await cancelDirectReservation(id, reservation)
        return true
      }
    }
    post({
      t: 'progress',
      id,
      sent: Math.min(item.file.size, offset + size),
      total: item.file.size,
      rate: 0,
      etaSec: 0
    })
  }
  if (pendingControls.get(id) === 'canceled') {
    pendingControls.delete(id)
    await cancelDirectReservation(id, reservation)
    return true
  }
  const final = await transport.completeDirect(reservation.id, completed)
  if (final.state !== 'complete' && final.state !== 'completed')
    throw new UploadHttpError(502, 'direct upload did not complete')
  directReservations.delete(id)
  directAborts.delete(id)
  post({
    t: 'done',
    id,
    dest: item.dest,
    size: item.file.size
  })
  return true
}

async function addFile(item: AddItem): Promise<void> {
  const id = item.id || `f-${Math.random().toString(36).slice(2, 10)}`
  // Posted before any await so a setup failure has a tray row to attach to.
  post({ t: 'queued', id, name: item.file.name, dest: item.dest, total: item.file.size })
  let handedOff = false

  try {
    try {
      if (directCapability && transport.reserveDirect) {
        const direct = await tryDirectUpload(item, id)
        if (direct) {
          handedOff = true
          return
        }
      }
    } catch {
      const reservation = directReservations.get(id)
      if (reservation) await cleanupDirectReservation(id, reservation)
      post({ t: 'error', id, code: 'upload.failed', message: /* i18n */ 'upload.upload_failed_out_retries' })
      return
    }
    if (pendingControls.get(id) === 'canceled') {
      pendingControls.delete(id)
      post({ t: 'canceled', id })
      return
    }
    const key = resumeKeyOf(item)

    await acquireSessionSlot()
    let sessionId = ''
    let resumeOffset = 0
    let chunkSize = startingChunkSize()
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
        resumeRecord = key ? resumeRecordFor(item, key, sessionId, chunkSize) : undefined
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

    startFile({
      id,
      file: item.file,
      dest: item.dest,
      encrypted: item.encrypted === true,
      chunkSize,
      sessionId,
      resumeRecord,
      resumeOffset,
      paused: intent === 'paused'
    })
    handedOff = true
  } finally {
    if (!handedOff) releasePreparedItem(item, id)
  }
}

self.addEventListener('message', (ev: MessageEvent<Cmd>) => {
  const cmd = ev.data
  switch (cmd.t) {
    case 'csrf':
      setCsrfToken(cmd.token)
      break
    case 'limits':
      setChunkLimits(cmd.chunkMin, cmd.chunkDefault)
      break
    case 'chunk-size':
      setChunkSizeOverride(cmd.size)
      break
    case 'direct-capability':
      directCapability = cmd.supported
      break
    case 'add':
      void ensureCleanupRecovery().then(() => {
        for (const item of cmd.items) void addFile(item)
      })
      break
    case 'concurrency':
      setConcurrency(cmd.maxInflight)
      break
    case 'pause':
      if (!pauseFile(cmd.id) && pendingControls.get(cmd.id) !== 'canceled') pendingControls.set(cmd.id, 'paused')
      break
    case 'resume':
      if (!resumeFile(cmd.id) && pendingControls.get(cmd.id) !== 'canceled') pendingControls.delete(cmd.id)
      break
    case 'cancel': {
      const direct = directReservations.get(cmd.id)
      if (direct) {
        pendingControls.set(cmd.id, 'canceled')
        directAborts.get(cmd.id)?.abort()
        void cancelDirectReservation(cmd.id, direct)
        break
      }
      if (!cancelFile(cmd.id)) pendingControls.set(cmd.id, 'canceled')
      break
    }
  }
})
