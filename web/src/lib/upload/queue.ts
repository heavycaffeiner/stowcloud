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
import { loadStoredConcurrency, storeConcurrency } from './chunk-planner'
import type { AddItem, Cmd, Evt } from './worker'

let worker: Worker | null = null

function handle(evt: Evt): void {
  switch (evt.t) {
    case 'queued':
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
      break
    case 'progress': {
      // A chunk already on the wire when pause or cancel was pressed still
      // reports afterwards. Taking the status from it would undo what the
      // person just asked for; the bytes are still true and still recorded.
      const current = uploads.statusOf(evt.id)
      const status = current === 'paused' || current === 'canceled' ? current : 'uploading'
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
      uploads.patch(evt.id, { status: evt.retryIn ? 'uploading' : 'error', message: evt.message })
      break
    case 'chunk-size-adjusted':
      uploads.patch(evt.id, {
        message: /* i18n */ 'upload.chunk_size_adjusted_mb',
        messageParams: { size: bytesToMb(evt.size) }
      })
      break
    case 'canceled':
      uploads.patch(evt.id, { status: 'canceled' })
      break
  }
}

function send(cmd: Cmd): void {
  if (worker === null) {
    worker = new Worker(new URL('./worker.ts', import.meta.url), { type: 'module' })
    worker.addEventListener('message', (ev: MessageEvent<Evt>) => handle(ev.data))
  }
  const session = queryClient.getQueryData<SessionInfo>(keys.session())
  worker.postMessage({ t: 'csrf', token: session?.csrf ?? '' } satisfies Cmd)
  if (session?.limits) {
    worker.postMessage({
      t: 'limits',
      chunkMin: session.limits.chunk_min,
      chunkDefault: session.limits.chunk_size
    } satisfies Cmd)
  }
  worker.postMessage({ t: 'concurrency', maxInflight: loadStoredConcurrency() } satisfies Cmd)
  worker.postMessage(cmd)
}

/**
 * `file`, encrypted for upload when `dest`'s share is encrypted, unchanged
 * otherwise. The whole file is read and encrypted here, on the main thread,
 * before anything reaches the upload Worker: rclone-crypt's on-disk block
 * boundaries have nothing to do with this app's HTTP chunk boundaries, so
 * encrypting one HTTP chunk independently of the others would not produce a
 * file rclone can read. `sendChunk` in `./worker.ts` slices whatever `File`
 * it is given at arbitrary byte offsets already; handing it the whole
 * ciphertext up front, wrapped as a same-named `File`, is the one place
 * that transformation happens and needs nothing downstream to change.
 *
 * Throws `LockedSessionError` (this share's key is not the unlocked one,
 * whether because nothing is unlocked or because a different share is) or
 * `FileTooLargeError` (over `MAX_ENCRYPTABLE_BYTES`) for the caller to turn
 * into a queued, failed row rather than silently uploading plaintext into an
 * encrypted share, encrypting under the wrong share's key, or crashing the
 * tab on a huge one.
 */
async function maybeEncrypt(file: File, dest: string): Promise<File> {
  const encryption = await encryptionForLabel(shareLabelOf(dest))
  if (encryption === null) return file
  const ciphertext = await encryptForUpload(file, encryption.salt)
  return new File([new Uint8Array(ciphertext)], file.name, { lastModified: file.lastModified })
}

/** The i18n key `toAddItem`'s catch turns an encryption failure into, or
 *  `null` for anything unrecognized (surfaced as the generic upload
 *  failure, same as any other unexpected error already is). */
function encryptionFailureMessage(err: unknown): string {
  if (err instanceof LockedSessionError) return /* i18n */ 'upload.share_locked'
  if (err instanceof FileTooLargeError) return /* i18n */ 'upload.file_too_large_to_encrypt'
  return /* i18n */ 'upload.encryption_failed'
}

/**
 * One file, encrypted if its destination needs it and turned into the
 * Worker's own `AddItem` shape, or `null` when encryption failed. A `null`
 * here already queued its own row and patched it to `error`: `addFiles`
 * only has to drop it from the batch handed to the Worker, not report it
 * again.
 */
async function toAddItem(file: File, dest: string, relativePath?: string): Promise<AddItem | null> {
  try {
    const encrypted = await maybeEncrypt(file, dest)
    return relativePath ? { file: encrypted, dest, relativePath } : { file: encrypted, dest }
  } catch (err) {
    const id = `f-${Math.random().toString(36).slice(2, 10)}`
    const name = relativePath ? relativePath.split('/').pop()! : file.name
    uploads.queue({ id, name, dest, total: file.size, sent: 0, rate: 0, etaSec: Infinity, status: 'uploading' })
    uploads.patch(id, { status: 'error', message: encryptionFailureMessage(err) })
    return null
  }
}

export async function addFiles(files: FileList | readonly File[], dest: string): Promise<void> {
  const items = await Promise.all(Array.from(files).map((file) => toAddItem(file, dest)))
  const ready = items.filter((item): item is AddItem => item !== null)
  if (ready.length === 0) return
  send({ t: 'add', items: ready })
}

export async function addEntries(entries: readonly { file: File; relativePath: string }[], dest: string): Promise<void> {
  const items = await Promise.all(entries.map((entry) => toAddItem(entry.file, dest, entry.relativePath)))
  const ready = items.filter((item): item is AddItem => item !== null)
  if (ready.length === 0) return
  send({ t: 'add', items: ready })
}

export function pauseUpload(id: string): void {
  uploads.patch(id, { status: 'paused' })
  send({ t: 'pause', id })
}

export function resumeUpload(id: string): void {
  uploads.patch(id, { status: 'uploading' })
  send({ t: 'resume', id })
}

export function cancelUpload(id: string): void {
  send({ t: 'cancel', id })
}

export function setUploadConcurrency(value: number): void {
  storeConcurrency(value)
  send({ t: 'concurrency', maxInflight: loadStoredConcurrency() })
}
