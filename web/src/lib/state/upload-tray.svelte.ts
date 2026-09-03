// web/src/lib/state/upload-tray.svelte.ts — UploadTray state, backed by the
// dedicated upload Worker. Survives route changes
// because it is instantiated once at the app root, not per-page.
import { api } from '../api/client'
import { bytesToMb } from '../format/bytes'
import { loadStoredConcurrency, storeConcurrency } from '../upload/chunk-planner'
import type { AddItem, Cmd, Evt } from '../upload/worker'
import { authState } from './auth.svelte'

export interface UploadItem {
  id: string
  name: string
  dest: string
  total: number
  sent: number
  rate: number
  etaSec: number
  status: 'queued' | 'uploading' | 'paused' | 'done' | 'error' | 'canceled'
  /** A msgid, not display text — `UploadTray.svelte` runs it through `t()`.
   *  The worker that produces most of them is off-thread and has no locale
   *  state, so it posts the Korean source string and translation happens at
   *  the one place that renders it. */
  message?: string
  messageParams?: Record<string, string | number>
}

export class UploadTrayState {
  items = $state<UploadItem[]>([])
  open = $state(false)
  #worker: Worker | null = null
  #onFileDone: ((dest: string) => void) | null = null

  #ensureWorker(): Worker {
    if (this.#worker) return this.#worker
    const w = new Worker(new URL('../upload/worker.ts', import.meta.url), { type: 'module' })
    w.addEventListener('message', (ev: MessageEvent<Evt>) => this.#handle(ev.data))
    this.#worker = w
    return w
  }

  /** Called by the browser page so a finished upload is reflected without a manual reload. */
  onFileDone(cb: (dest: string) => void): void {
    this.#onFileDone = cb
  }

  #patch(id: string, patch: Partial<UploadItem>): void {
    const i = this.items.findIndex((x) => x.id === id)
    if (i === -1) return
    this.items[i] = { ...this.items[i], ...patch }
  }

  #handle(evt: Evt): void {
    switch (evt.t) {
      case 'queued':
        this.items = [
          ...this.items,
          { id: evt.id, name: evt.name, dest: evt.dest, total: evt.total, sent: 0, rate: 0, etaSec: Infinity, status: 'uploading' }
        ]
        this.open = true
        break
      case 'progress': {
        // A chunk that was already on the wire when pause or cancel was
        // pressed still reports its progress afterwards. Taking the status
        // from it would undo what the person just asked for: the row went
        // back to "uploading" a moment after being paused, so the pause
        // control never appeared to work. The bytes are still true and are
        // still recorded.
        const cur = this.items.find((x) => x.id === evt.id)?.status
        const status = cur === 'paused' || cur === 'canceled' ? cur : 'uploading'
        this.#patch(evt.id, { sent: evt.sent, total: evt.total, rate: evt.rate, etaSec: evt.etaSec, status })
        break
      }
      case 'done': {
        this.#patch(evt.id, { sent: evt.size, status: 'done' })
        api.registerUploadedEntry(evt.dest, {
          name: evt.name,
          // The server addresses it without the leading slash the destination
          // carries; the next listing replaces this row with the real one.
          path: `${evt.dest}/${evt.name}`.replace(/^\/+/, '').replace(/\/{2,}/g, '/'),
          kind: 'file',
          size: evt.size,
          mtime_ns: evt.mtimeNs,
          // A locally minted token for a row this client just created. It is
          // not the server's, so it is never exact.
          etag: Math.random().toString(16).slice(2),
          etag_weak: true,
          perms: { read: true, write: true, create: false, delete: true, rename: true, move: true, share: true, download: true },
          // No numeric fid for a freshly-uploaded file — same reason a plain
          // `list`/`stat` doesn't have one (`Entry.id`'s doc comment in
          // `types.ts`). Left `undefined` rather than a fabricated string
          // that would no longer even type-check against `id?: number`.
          id: undefined
        })
        this.#onFileDone?.(evt.dest)
        break
      }
      case 'error':
        this.#patch(evt.id, { status: evt.retryIn ? 'uploading' : 'error', message: evt.message })
        break
      case 'chunk-size-adjusted':
        this.#patch(evt.id, {
          message: /* i18n */ 'upload.chunk_size_adjusted_mb',
          messageParams: { size: bytesToMb(evt.size) }
        })
        break
      case 'canceled':
        this.#patch(evt.id, { status: 'canceled' })
        break
    }
  }

  #send(cmd: Cmd): void {
    const w = this.#ensureWorker()
    // The worker is a separate module realm from `api/http.ts`, so it can't
    // see that module's csrfToken — resend ours with every command (add,
    // pause/resume, cancel-which-DELETEs) so it's never stale, including
    // across a re-login that rotates the session/csrf without a page reload.
    w.postMessage({ t: 'csrf', token: authState.session?.csrf ?? '' } satisfies Cmd)
    // Same reason as csrf above: the worker's module-scoped chunk-size
    // fallback can't see `authState` directly, so the server's configured
    // floor/default (`GET /api/auth/session`'s `limits`) has to be resent
    // alongside every command too.
    const limits = authState.session?.limits
    if (limits) {
      w.postMessage({ t: 'limits', chunkMin: limits.chunk_min, chunkDefault: limits.chunk_size } satisfies Cmd)
    }
    w.postMessage({ t: 'concurrency', maxInflight: loadStoredConcurrency() } satisfies Cmd)
    w.postMessage(cmd)
  }

  addFiles(fileList: FileList | File[], dest: string): void {
    const items: AddItem[] = Array.from(fileList).map((file) => ({ file, dest }))
    if (items.length === 0) return
    this.#send({ t: 'add', items })
  }

  addEntries(entries: { file: File; relativePath: string }[], dest: string): void {
    const items: AddItem[] = entries.map((e) => ({ file: e.file, dest, relativePath: e.relativePath }))
    if (items.length === 0) return
    this.#send({ t: 'add', items })
  }

  pause(id: string): void {
    this.#patch(id, { status: 'paused' })
    this.#send({ t: 'pause', id })
  }

  resume(id: string): void {
    this.#patch(id, { status: 'uploading' })
    this.#send({ t: 'resume', id })
  }

  cancel(id: string): void {
    this.#send({ t: 'cancel', id })
  }

  dismiss(id: string): void {
    this.items = this.items.filter((x) => x.id !== id)
  }

  clearFinished(): void {
    this.items = this.items.filter((x) => x.status !== 'done' && x.status !== 'canceled')
  }

  setConcurrency(val: number): void {
    storeConcurrency(val)
    this.#send({ t: 'concurrency', maxInflight: loadStoredConcurrency() })
  }
}

export const uploadTray = new UploadTrayState()
