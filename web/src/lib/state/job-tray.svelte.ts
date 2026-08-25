// web/src/lib/state/job-tray.svelte.ts — JobTray state, backed by `pollJob`
// (REST poll + WebSocket `job` push reconciled, `state/jobs.ts`). Mirrors
// `upload-tray.svelte.ts`'s pattern: a singleton class instantiated once at
// the app root, so a job started on one page keeps reporting progress after
// the user navigates away — same reason UploadTray survives route changes.
import { api } from '../api/client'
import type { BatchResult } from '../api/types'
import type { JobKindWire, JobStatus } from '../api/client'
import { JobFailedError, JobTimeoutError, pollJob } from './jobs'

export type JobKind = 'delete' | 'copy' | 'index'
export type JobItemStatus = 'running' | 'done' | 'error' | 'cancelled' | 'interrupted'

export interface JobItem {
  id: string
  kind: JobKind
  done: number
  total: number
  status: JobItemStatus
  /** A catalogue key, never text: the first server-reported failure's, or —
   *  for the timeout note this module raises itself — its own. The tray
   *  resolves it at render time, in the reader's language. */
  message?: string
  /** `{name}` holes for [`message`], when the key has any. */
  messageParams?: Record<string, string>
  /** Paths the server started but never recorded an outcome for — the
   *  process died between `begin_result` and `finish_result`, so whether the
   *  file was moved/copied/deleted is genuinely unknown and only a look at
   *  the destination can settle it. At most one entry in practice. */
  attempting?: string[]
  /** Paths the job was asked for but never reached. Untouched on disk, so
   *  re-running the same operation on exactly these is safe — which is the
   *  only reason the tray can offer them as a to-do list at all. */
  pending?: string[]
}

// The only place the frontend's own `JobKind` (also used for tray icon/label
// lookup) and the backend's wire `JobKindWire` (`"index_build"` vs `"index"`)
// diverge. Every other value is spelled identically in both.
function wireKind(kind: JobKind): JobKindWire {
  return kind === 'index' ? 'index_build' : kind
}

// A kind this build has no tray row for still has to land somewhere: an
// archive streams and a move finishes inline, so neither creates a job here,
// but an older server's rows are still readable and are shown as a copy
// rather than dropped.
function frontendKind(kind: JobKindWire): JobKind {
  switch (kind) {
    case 'index_build':
      return 'index'
    case 'delete':
      return 'delete'
    default:
      return 'copy'
  }
}

export class JobTrayState {
  items = $state<JobItem[]>([])
  open = $state(false)
  /** True once a `GET /api/jobs` re-attach fetch has failed — the server is
   *  unreachable (e.g. mid-restart during a Docker cutover) and `items`
   *  reflects however things stood at the last successful fetch, not
   *  necessarily reality right now. `JobTray` shows a banner for this rather
   *  than clearing the tray, since an empty tray reads as "nothing is
   *  running" and that is not a claim this state can back up while it
   *  cannot see the server. */
  stale = $state(false)

  #patch(id: string, patch: Partial<JobItem>): void {
    const i = this.items.findIndex((x) => x.id === id)
    if (i === -1) return
    this.items[i] = { ...this.items[i], ...patch }
  }

  /** Shared by `track()` (a job this tab just started) and `attachOpenJobs()`
   *  (a job discovered already running, re-attached to). Resolves with the
   *  same `BatchResult` shape once the job reaches `done`; rejects with
   *  `JobFailedError`/`JobTimeoutError` otherwise — a `cancelled`/
   *  `interrupted` job still resolves, with whatever partial `results` it
   *  reached, since that's a real (if incomplete) outcome, not a
   *  caller-facing failure. */
  async #poll(id: string, kind: JobKind): Promise<BatchResult> {
    try {
      const status = await pollJob(id, wireKind(kind), (s) => this.#patch(id, { done: s.done, total: s.total }))
      this.#patch(id, { done: status.done, total: status.total, status: 'done' })
      return { results: status.results }
    } catch (err) {
      if (err instanceof JobFailedError) {
        // `cancelled` (a user asked to stop) and `interrupted` (the process
        // hosting the runner died mid-job, `JobStore::open`'s sweep) are
        // different facts and get their own tray copy — collapsing them
        // would tell whoever asked to cancel a job that the server crashed,
        // or the other way round.
        const itemStatus: JobItemStatus =
          err.status.state === 'cancelled' ? 'cancelled' : err.status.state === 'interrupted' ? 'interrupted' : 'error'
        this.#patch(id, {
          done: err.status.done,
          total: err.status.total,
          status: itemStatus,
          // `err.key` is the first failed item's catalogue key. Null when the
          // job failed without a per-item result (a 404 mid-poll — the row was
          // GC'd), and the tray then says only that the job failed, which is
          // all that is actually known.
          message: err.key ?? undefined,
          messageParams: err.params,
          // Carried through so the tray can name what did not happen. A job
          // that stops without finishing is only "not lost" if the user can
          // see which items are still outstanding.
          attempting: err.status.attempting,
          pending: err.status.pending
        })
        if (itemStatus !== 'error') return { results: err.status.results }
        throw err
      }
      if (err instanceof JobTimeoutError) {
        // A catalogue key, not display text — `JobTray.svelte` resolves it.
        this.#patch(id, { status: 'error', message: /* i18n */ 'job.could_not_check_job_status' })
      }
      throw err
    }
  }

  /** Starts tracking a job the server just answered with `202 { job }`. */
  async track(id: string, kind: JobKind): Promise<BatchResult> {
    this.items = [...this.items, { id, kind, done: 0, total: 0, status: 'running' }]
    this.open = true
    return this.#poll(id, kind)
  }

  /**
   * Re-attaches to every non-terminal job the server still knows about —
   * called once from `JobTray`'s `onMount`. Deliberately not gated on any
   * client-side record of which ids to look for: a browser refresh clears
   * `items`, and a server restart (a Docker container cutover) clears the
   * server's own in-memory runners too, so the only durable list of "what's
   * still open" is `jobs.db` itself, read fresh via `GET /api/jobs` every
   * time. A job already re-attached (rare — this only runs once per mount)
   * is left alone rather than double-tracked.
   *
   * A `running` job resumes live polling exactly like one this tab started
   * itself. An `interrupted` job (the process that owned it died mid-run,
   * never resumed) is shown as its own terminal status rather than dropped
   * silently or folded into `cancelled` — nobody asked to cancel it.
   *
   * If the fetch itself fails, `items` is left exactly as it was and
   * `stale` is raised instead of being cleared — see that field's doc.
   */
  async attachOpenJobs(): Promise<void> {
    let jobs: JobStatus[]
    try {
      jobs = (await api.jobList()).jobs
    } catch {
      this.stale = true
      return
    }
    this.stale = false
    for (const job of jobs) {
      if (this.items.some((x) => x.id === job.id)) continue
      const kind = frontendKind(job.kind)
      if (job.state === 'interrupted') {
        this.items = [
          ...this.items,
          {
            id: job.id,
            kind,
            done: job.done,
            total: job.total,
            status: 'interrupted',
            attempting: job.attempting,
            pending: job.pending
          }
        ]
        this.open = true
        continue
      }
      this.items = [...this.items, { id: job.id, kind, done: job.done, total: job.total, status: 'running' }]
      this.open = true
      // Fire-and-forget, same as a `downloadAsArchive`/`doDelete` caller
      // that doesn't await `track()` — nobody here is waiting on the
      // result, `#poll` already patches `items` as it goes.
      void this.#poll(job.id, kind).catch(() => {})
    }
  }

  /** `DELETE /api/jobs/{id}` — cancels at the next item boundary, not
   *  mid-file. Does not itself patch `items`; the `track()` call already
   *  watching this id sees the resulting `cancelled` terminal state through
   *  its own `pollJob` (REST poll or the WS push, whichever lands first). */
  async cancel(id: string): Promise<void> {
    await api.jobCancel(id)
  }

  dismiss(id: string): void {
    this.items = this.items.filter((x) => x.id !== id)
  }

  clearFinished(): void {
    this.items = this.items.filter((x) => x.status === 'running')
  }
}

export const jobTray = new JobTrayState()
