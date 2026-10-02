// Server sessions and direct reservations this worker still owes a DELETE. Each id is recorded before the
// request goes out, so an uncertain cancellation is retried later instead of leaking the reservation.
import {
  cleanupKey,
  deleteCleanupRecord,
  deleteResumeRecord,
  getCleanupRecords,
  putCleanupRecord,
  putResumeRecord,
  type CleanupRecord,
  type ResumeRecord
} from './persistence'
import { post } from './protocol'
import { statusOf, transport } from './transport'

/** In-memory copies keep an uncertain session recoverable for this worker's
 * lifetime even when IndexedDB is temporarily unavailable. The persisted copy
 * is still written whenever the browser storage allows it. */
const pendingCleanupRecords = new Map<string, CleanupRecord>()
const cleanupInFlight = new Map<string, Promise<boolean>>()
let cleanupRecoveryPromise: Promise<void> | null = null

const CLEANUP_PENDING_CODE = /* i18n */ 'upload.cleanup_pending'
const CLEANUP_PENDING_MESSAGE = /* i18n */ 'upload.cleanup_pending'

export function postCleanupPending(id: string): void {
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

async function deleteDirectConfirmed(reservationId: string): Promise<boolean> {
  if (!transport.cancelDirect) return false
  try {
    await transport.cancelDirect(reservationId)
    return true
  } catch (err) {
    return statusConfirmsRemoval(err)
  }
}

function cleanupRecordFor(sessionId: string, direct = false): CleanupRecord {
  const existing = pendingCleanupRecords.get(sessionId)
  if (existing) return existing
  const record = { key: cleanupKey(sessionId), sessionId, updatedAt: Date.now(), ...(direct ? { direct: true } : {}) }
  pendingCleanupRecords.set(sessionId, record)
  return record
}

export async function rememberCleanup(sessionId: string, direct = false): Promise<CleanupRecord> {
  const record = cleanupRecordFor(sessionId, direct)
  await putCleanupRecord(record).catch(() => {})
  return record
}

export async function forgetCleanup(sessionId: string): Promise<void> {
  const record = pendingCleanupRecords.get(sessionId)
  pendingCleanupRecords.delete(sessionId)
  await deleteCleanupRecord(record?.key ?? cleanupKey(sessionId)).catch(() => {})
}

/** Resolves true only once the server confirms the session or reservation is gone. Concurrent calls for
 * one id share a request. */
export function cleanupSession(sessionId: string, record?: ResumeRecord, direct = false): Promise<boolean> {
  if (!sessionId) return Promise.resolve(true)
  const running = cleanupInFlight.get(sessionId)
  if (running) return running

  const run = (async (): Promise<boolean> => {
    try {
      await rememberCleanup(sessionId, direct)
      if (record) await putResumeRecord({ ...record, cleanupPending: true }).catch(() => {})
      const removed = direct ? await deleteDirectConfirmed(sessionId) : await deleteSessionConfirmed(sessionId)
      if (!removed) return false
      if (record) await deleteResumeRecord(record.key).catch(() => {})
      await forgetCleanup(sessionId)
      return true
    } catch {
      // Cleanup failures are represented by the pending result. The id remains
      // in memory and in IndexedDB whenever storage is available.
      return false
    }
  })()
  cleanupInFlight.set(sessionId, run)
  void run.finally(() => {
    if (cleanupInFlight.get(sessionId) === run) cleanupInFlight.delete(sessionId)
  })
  return run
}

/** DELETE is confirmation of cancellation only when it resolves or explicitly
 * reports that the session is gone. An uncertain request leaves both the
 * resumable record and the independent cleanup record intact. */
export async function cleanupServerSession(record: ResumeRecord): Promise<boolean> {
  return cleanupSession(record.sessionId, record)
}

export async function cleanupCreatedSession(sessionId: string, record?: ResumeRecord): Promise<boolean> {
  if (!sessionId) return true
  return cleanupSession(sessionId, record)
}

/** Retry cleanup records retained by an earlier worker instance. A recovery
 * attempt is deliberately serial so a burst of stale sessions cannot turn
 * into another request storm. */
async function recoverPendingCleanups(): Promise<void> {
  const persisted = await getCleanupRecords().catch(() => [])
  const records = new Map<string, CleanupRecord>()
  for (const record of pendingCleanupRecords.values()) records.set(record.sessionId, record)
  for (const record of persisted) records.set(record.sessionId, record)
  for (const record of records.values()) await cleanupSession(record.sessionId, undefined, record.direct === true)
}

export function ensureCleanupRecovery(): Promise<void> {
  if (cleanupRecoveryPromise) return cleanupRecoveryPromise
  const run = recoverPendingCleanups().catch(() => {})
  cleanupRecoveryPromise = run
  void run.finally(() => {
    if (cleanupRecoveryPromise === run) cleanupRecoveryPromise = null
  })
  return run
}
