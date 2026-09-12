// The upload Worker's other half: it owns the Worker, translates its events
// into store writes, and invalidates the directory a finished file landed in.
//
// The Worker is a separate module realm, so it cannot see the CSRF token or
// the server's chunk limits. Both are resent with every command, which is also
// what keeps them right across a re-login that rotates the session.
import { api } from '../api/client'
import type { SessionInfo } from '../api/types'
import { encryptionForLabel, shareLabelOf } from '../crypto/encrypted-shares'
import { encryptForUpload, FileTooLargeError, LockedSessionError } from '../crypto/e2ee'
import { bytesToMb } from '../format/bytes'
import { queryClient } from '../query/client'
import { invalidateDirs } from '../query/files'
import { keys } from '../query/keys'
import { uploads } from '../store/upload.store'
import { CHUNK_SIZE_MIN, loadStoredChunkSize, loadStoredConcurrency, storeChunkSize, storeConcurrency, subscribeUploadPreferences } from './chunk-planner'
import type { AddItem, Cmd, Evt } from './worker'

let worker: Worker | null = null

export function handle(evt: Evt): void {
  switch (evt.t) {
    case 'queued': {
      // Preparation rows are registered by addFiles before the worker gets an
      // item. Keep that row and preserve a control pressed during preparation;
      // only create a row here for callers that talk to an older queue.
      const current = uploads.statusOf(evt.id)
      if (current === undefined) {
        uploads.queue({
          id: evt.id,
          name: evt.name,
          dest: evt.dest,
          total: evt.total,
          sent: 0,
          rate: 0,
          etaSec: Infinity,
          status: 'uploading'
        })
      } else {
        uploads.patch(evt.id, {
          name: evt.name,
          dest: evt.dest,
          total: evt.total,
          status: current === 'paused' || current === 'canceled' ? current : 'uploading'
        })
      }
      break
    }
    case 'progress': {
      // A chunk already on the wire when pause or cancel was pressed still
      // reports afterwards. Taking the status from it would undo what the
      // person just asked for; the bytes are still true and still recorded.
      const current = uploads.statusOf(evt.id)
      const status =
        current === 'paused' || current === 'canceled' || current === 'error' ? current : 'uploading'
      uploads.patch(evt.id, { sent: evt.sent, total: evt.total, rate: evt.rate, etaSec: evt.etaSec, status })
      break
    }
    case 'done':
      uploads.patch(evt.id, { sent: evt.size, status: 'done' })
      // A no-op against the real server, whose own state is authoritative;
      // the mock backend has no other way to learn the file exists.
      api.registerUploadedEntry(evt.dest, {
        name: evt.name,
        path: `${evt.dest}/${evt.name}`.replace(/\/{2,}/g, '/').replace(/^\/+/, ''),
        kind: 'file',
        size: evt.size,
        mtime_ns: evt.mtimeNs,
        // Locally minted for a row this client just created, so never exact.
        etag: Math.random().toString(16).slice(2),
        etag_weak: true,
        perms: { read: true, write: true, create: false, delete: true, rename: true, move: true, share: true, download: true },
        id: undefined
      })
      // What makes the new file appear wherever it was uploaded to, in every
      // screen showing that directory.
      invalidateDirs([evt.dest])
      break
    case 'error':
      uploads.patch(evt.id, {
        status: evt.retryIn ? 'uploading' : 'error',
        message: evt.message,
        errorCode: evt.code
      })
      break
    case 'chunk-size-adjusted':
      storeChunkSize(evt.size, currentChunkMin())
      uploads.patch(evt.id, {
        message: /* i18n */ 'upload.chunk_size_adjusted_mb',
        messageParams: { size: bytesToMb(evt.size) }
      })
      break
    case 'canceled':
      uploads.patch(evt.id, { status: 'canceled', message: undefined, errorCode: undefined })
      break
    case 'released':
      releaseEncryptedPermit(evt.id)
      break
  }
}

function currentChunkMin(): number {
  return queryClient.getQueryData<SessionInfo>(keys.session())?.limits.chunk_min ?? CHUNK_SIZE_MIN
}

function syncWorkerPreferences(): void {
  if (worker === null) return
  const session = queryClient.getQueryData<SessionInfo>(keys.session())
  if (session?.limits) {
    worker.postMessage({
      t: 'limits',
      chunkMin: session.limits.chunk_min,
      chunkDefault: session.limits.chunk_size
    } satisfies Cmd)
  }
  worker.postMessage({ t: 'chunk-size', size: loadStoredChunkSize(currentChunkMin()) } satisfies Cmd)
  worker.postMessage({ t: 'concurrency', maxInflight: loadStoredConcurrency() } satisfies Cmd)
}

function send(cmd: Cmd): void {
  if (worker === null) {
    worker = new Worker(new URL('./worker.ts', import.meta.url), { type: 'module' })
    worker.addEventListener('message', (ev: MessageEvent<Evt>) => handle(ev.data))
    subscribeUploadPreferences(syncWorkerPreferences)
  }
  const session = queryClient.getQueryData<SessionInfo>(keys.session())
  worker.postMessage({ t: 'csrf', token: session?.csrf ?? '' } satisfies Cmd)
  syncWorkerPreferences()
  worker.postMessage(cmd)
}

function sendPrepared(items: readonly AddItem[]): void {
  try {
    send({ t: 'add', items: [...items] })
  } catch (err) {
    for (const item of items) {
      if (item.encrypted) releaseEncryptedPermit(item.id)
    }
    throw err
  }
  for (const item of items) {
    // Reconcile controls after the add message is ordered. The worker records
    // these commands even while its asynchronous session setup is still
    // yielding, so a resume that follows a pause cannot be overwritten by a
    // stale preparation snapshot.
    const status = uploads.statusOf(item.id)
    if (status === 'paused') send({ t: 'pause', id: item.id })
    else if (status === 'canceled') send({ t: 'cancel', id: item.id })
  }
}

/** At most one whole-file encryption runs at once. Plain files only hold their
 * browser File handle and do not wait for this preparation slot. */
export const MAX_PREPARATION_CONCURRENCY = 1
let preparationTail: Promise<void> = Promise.resolve()

interface EncryptedPermitWaiter {
  id: string
  resolve: (granted: boolean) => void
}

// A prepared ciphertext keeps this permit until the worker releases its File
// after completion, a permanent error, or cancellation. This bounds aggregate
// retained ciphertext as well as the transient encryption itself.
let encryptedPermitOwner: string | null = null
const encryptedPermitWaiters: EncryptedPermitWaiter[] = []

function acquireEncryptedPermit(id: string): Promise<boolean> {
  if (encryptedPermitOwner === null && encryptedPermitWaiters.length === 0) {
    encryptedPermitOwner = id
    return Promise.resolve(true)
  }
  const { promise, resolve } = Promise.withResolvers<boolean>()
  encryptedPermitWaiters.push({ id, resolve })
  return promise
}


function releaseEncryptedPermit(id: string): void {
  if (encryptedPermitOwner !== id) return
  const next = encryptedPermitWaiters.shift()
  if (next) {
    encryptedPermitOwner = next.id
    next.resolve(true)
  } else {
    encryptedPermitOwner = null
  }
}

function cancelEncryptedPermit(id: string): void {
  const waiting = encryptedPermitWaiters.findIndex((waiter) => waiter.id === id)
  if (waiting === -1) return
  const [waiter] = encryptedPermitWaiters.splice(waiting, 1)
  waiter.resolve(false)
}

async function inPreparationSlot<T>(work: () => Promise<T>): Promise<T> {
  let release!: () => void
  const turn = preparationTail
  preparationTail = new Promise<void>((resolve) => {
    release = resolve
  })
  await turn
  try {
    return await work()
  } finally {
    release()
  }
}

interface PreparationState {
  id: string
  paused: boolean
  canceled: boolean
}

const preparations = new Map<string, PreparationState>()

function newPreparation(file: File, dest: string): PreparationState {
  const id =
    typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function'
      ? crypto.randomUUID()
      : `upload-${Math.random().toString(36).slice(2, 10)}`
  const state: PreparationState = { id, paused: false, canceled: false }
  preparations.set(id, state)
  uploads.queue({
    id,
    name: file.name,
    dest,
    total: file.size,
    sent: 0,
    rate: 0,
    etaSec: Infinity,
    status: 'uploading'
  })
  return state
}

interface UploadContext {
  accountId?: string
  sessionContext?: string
}

/** SHA-256 is only an identity marker. It is never used as a key or sent to
 * the server as a credential. If SubtleCrypto is unavailable, returning
 * undefined disables resume for this item rather than guessing identity. */
async function digestBytes(bytes: Uint8Array): Promise<string | undefined> {
  const subtle = globalThis.crypto?.subtle
  if (!subtle) return undefined
  try {
    const digest = await subtle.digest('SHA-256', bytes as unknown as BufferSource)
    return Array.from(new Uint8Array(digest), (b) => b.toString(16).padStart(2, '0')).join('')
  } catch {
    return undefined
  }
}

async function digestFile(file: Blob): Promise<string | undefined> {
  let bytes: Uint8Array | undefined
  try {
    bytes = new Uint8Array(await file.arrayBuffer())
    return await digestBytes(bytes)
  } catch {
    return undefined
  } finally {
    // This is a temporary identity buffer, not the File's backing storage.
    bytes?.fill(0)
  }
}

async function contextForSession(): Promise<UploadContext> {
  const session = queryClient.getQueryData<SessionInfo>(keys.session())
  const accountId = session?.user?.id === undefined ? undefined : String(session.user.id)
  if (!accountId || !session?.csrf) return { accountId }
  return { accountId, sessionContext: await digestBytes(new TextEncoder().encode(session.csrf)) }
}

/** `file`, encrypted for upload when `dest`'s share is encrypted, unchanged
 * otherwise. The whole file is read and encrypted here, on the main thread,
 * before anything reaches the upload Worker: rclone-crypt's on-disk block
 * boundaries have nothing to do with this app's HTTP chunk boundaries, so
 * encrypting one HTTP chunk independently of the others would not produce a
 * file rclone can read. `sendChunk` in `./worker.ts` slices whatever `File`
 * it is given at arbitrary byte offsets already; handing it the whole
 * ciphertext up front, wrapped as a same-named `File`, is the one place
 * that transformation happens and needs nothing downstream to change.
 */

function encryptionFailureMessage(err: unknown): string {
  if (err instanceof LockedSessionError) return /* i18n */ 'upload.share_locked'
  if (err instanceof FileTooLargeError) return /* i18n */ 'upload.file_too_large_to_encrypt'
  return /* i18n */ 'upload.encryption_failed'
}
async function maybeEncrypt(
  file: File,
  dest: string,
  state: PreparationState
): Promise<{ file: File; encrypted: boolean; ciphertextIdentity?: string } | null> {
  const encryption = await encryptionForLabel(shareLabelOf(dest))
  if (encryption === null) return { file, encrypted: false }
  if (state.canceled || !(await acquireEncryptedPermit(state.id))) return null
  if (state.canceled) {
    releaseEncryptedPermit(state.id)
    return null
  }
  try {
    const ciphertext = await encryptForUpload(file, encryption.salt)
    try {
      const ciphertextIdentity = await digestBytes(ciphertext)
      const encryptedFile = new File([new Uint8Array(ciphertext)], file.name, { lastModified: file.lastModified })
      return {
        file: encryptedFile,
        encrypted: true,
        ciphertextIdentity
      }
    } finally {
      ciphertext.fill(0)
    }
  } catch (err) {
    releaseEncryptedPermit(state.id)
    throw err
  }
}

/** One file, encrypted if its destination needs it and turned into the
 * Worker's AddItem shape, or null when preparation failed or was canceled. */
async function toAddItem(
  file: File,
  dest: string,
  relativePath: string | undefined,
  context: Promise<UploadContext>
): Promise<AddItem | null> {
  const state = newPreparation(file, dest)
  try {
    const prepared = await inPreparationSlot(async () => {
      if (state.canceled) return null
      const encrypted = await maybeEncrypt(file, dest, state)
      if (encrypted === null || state.canceled) {
        releaseEncryptedPermit(state.id)
        return null
      }
      const sourceIdentity = await digestFile(file)
      if (state.canceled) {
        releaseEncryptedPermit(state.id)
        return null
      }
      const contextIdentity = await context
      return { encrypted, sourceIdentity, contextIdentity }
    })
    if (state.canceled || prepared === null || prepared.encrypted === null) {
      releaseEncryptedPermit(state.id)
      return null
    }
    const ciphertextIdentity = prepared.encrypted.encrypted
      ? prepared.encrypted.ciphertextIdentity
      : prepared.sourceIdentity
    return {
      id: state.id,
      file: prepared.encrypted.file,
      encrypted: prepared.encrypted.encrypted,
      dest,
      sourceName: file.name,
      sourceSize: file.size,
      sourceLastModified: file.lastModified,
      ...(relativePath !== undefined ? { relativePath } : {}),
      ...(prepared.contextIdentity.accountId ? { accountId: prepared.contextIdentity.accountId } : {}),
      ...(prepared.contextIdentity.sessionContext ? { sessionContext: prepared.contextIdentity.sessionContext } : {}),
      ...(prepared.sourceIdentity ? { sourceIdentity: prepared.sourceIdentity } : {}),
      ...(ciphertextIdentity ? { ciphertextIdentity } : {}),
    }
  } catch (err) {
    releaseEncryptedPermit(state.id)
    if (!state.canceled) uploads.patch(state.id, { status: 'error', message: encryptionFailureMessage(err) })
    return null
  } finally {
    preparations.delete(state.id)
  }
}

export async function addFiles(files: FileList | readonly File[], dest: string): Promise<void> {
  const context = contextForSession()
  for (const file of Array.from(files)) {
    const item = await toAddItem(file, dest, undefined, context)
    if (item !== null) sendPrepared([item])
  }
}

export async function addEntries(entries: readonly { file: File; relativePath: string }[], dest: string): Promise<void> {
  const context = contextForSession()
  for (const entry of entries) {
    const item = await toAddItem(entry.file, dest, entry.relativePath, context)
    if (item !== null) sendPrepared([item])
  }
}

export function pauseUpload(id: string): void {
  const preparation = preparations.get(id)
  if (preparation) {
    if (preparation.canceled) return
    preparation.paused = true
    uploads.patch(id, { status: 'paused' })
    return
  }
  uploads.patch(id, { status: 'paused' })
  send({ t: 'pause', id })
}
export function resumeUpload(id: string): void {
  const preparation = preparations.get(id)
  if (preparation) {
    if (preparation.canceled) return
    preparation.paused = false
    uploads.patch(id, { status: 'uploading' })
    return
  }
  uploads.patch(id, { status: 'uploading' })
  send({ t: 'resume', id })
}

export function cancelUpload(id: string): void {
  const preparation = preparations.get(id)
  if (preparation) {
    // Blob.arrayBuffer() has no abort signal, so let the bounded preparation
    // finish but discard its result. No session can be created for it.
    preparation.canceled = true
    cancelEncryptedPermit(id)
    uploads.patch(id, { status: 'canceled' })
    return
  }
  send({ t: 'cancel', id })
}

export function setUploadChunkSize(value: number | null, min = currentChunkMin()): boolean {
  return storeChunkSize(value, min)
}

export function setUploadConcurrency(value: number): boolean {
  if (!storeConcurrency(value)) return false
  send({ t: 'concurrency', maxInflight: loadStoredConcurrency() })
  return true
}
