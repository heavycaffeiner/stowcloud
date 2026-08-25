// Progress polling for `GET /api/jobs/{id}`. `fs_move`/`fs_copy`/`fs_delete`/`fs_archive` produce a
// job id once a batch crosses the threshold (`go/internal/httpapi/handler/admin_ops.go`);
// `api/http.ts`'s `copy`/`del`/`archive` hand that `{ job }` envelope straight
// to the caller instead of awaiting it. `state/job-tray.svelte.ts` is that
// caller — it wraps `pollJob` to show live progress and offer cancellation
// while the job runs, surfaced by `ui/JobTray.svelte`.
import { api, ApiError, type JobKindWire, type JobStatus } from '../api/client'
import { batchErrorKey } from '../api/error-text'

const POLL_MS = 1000
// A job that hasn't reached a terminal state after this long is either a
// server bug (nothing ever finishes) or a poll aimed at an id that will
// never resolve — either way, a loop that never stops is worse than one
// that gives up loudly. 20 minutes covers the slowest plausible operation
// this product does today (a multi-hundred-GB cross-device move) with
// headroom, without polling forever against a hung server.
const MAX_POLL_MS = 20 * 60 * 1000

const TERMINAL: JobStatus['state'][] = ['done', 'error', 'cancelled', 'interrupted']

export class JobTimeoutError extends Error {
  constructor(id: string) {
    super(`job ${id} did not reach a terminal state within ${MAX_POLL_MS}ms`)
  }
}

export class JobFailedError extends Error {
  status: JobStatus
  /** The first failed item's catalogue key and placeholders, if the server
   *  reported one. `message` next to it is the English log line — the tray
   *  renders `key`, because it has no way to translate a sentence. */
  key: string | null
  params?: Record<string, string>
  constructor(status: JobStatus) {
    super(status.errors[0] ?? `job ended in state "${status.state}"`)
    this.status = status
    const first = batchErrorKey(status.results.find((r) => !r.ok)?.error)
    this.key = first?.key ?? null
    this.params = first?.params
  }
}

/**
 * Polls `GET /api/jobs/{id}` until it reaches a terminal state, calling
 * `onProgress` on every observed update. Polling is the only channel: the
 * hub sends invalidations, not job progress. Resolves with the final
 * `JobStatus` on `done`; rejects with `JobFailedError` for `error`/
 * `cancelled`/`interrupted` (all three are "did not complete successfully"
 * from a caller's point of view — `err.status.state` says which) or
 * `JobTimeoutError` if it never reaches one. Deliberately has no UI
 * dependency — callers report a rejection through their own `Snackbar`:
 *
 * ```ts
 * try {
 *   await pollJob(jobId, (s) => { progress = s })
 * } catch (e) {
 *   snackbarMsg = e instanceof JobFailedError && e.key ? t(e.key, e.params) : t('job.could_not_check_job_status')
 * }
 * ```
 */
export function pollJob(id: string, kind: JobKindWire, onProgress?: (status: JobStatus) => void): Promise<JobStatus> {
  return new Promise((resolve, reject) => {
    let settled = false
    let timer: ReturnType<typeof setTimeout> | null = null
    const deadline = Date.now() + MAX_POLL_MS

    function settle(fn: () => void): void {
      if (settled) return
      settled = true
      if (timer) clearTimeout(timer)
      fn()
    }

    async function tick(): Promise<void> {
      if (settled) return
      if (Date.now() > deadline) {
        settle(() => reject(new JobTimeoutError(id)))
        return
      }
      try {
        const status = await api.jobStatus(id)
        if (settled) return
        onProgress?.(status)
        if (TERMINAL.includes(status.state)) {
          settle(() => (status.state === 'done' ? resolve(status) : reject(new JobFailedError(status))))
          return
        }
      } catch (err) {
        // A 404 mid-poll (the row was GC'd, or a cancel raced this tick) is
        // terminal, not a reason to keep polling a job that is provably
        // gone. Anything else (a transient network blip) is worth one more
        // tick rather than failing the whole wait over a single hiccup.
        if (err instanceof ApiError && err.status === 404) {
          settle(() =>
            reject(
              new JobFailedError({
                id,
                kind,
                state: 'error',
                done: 0,
                total: 0,
                current: null,
                errors: [err.message],
                results: [],
                attempting: [],
                pending: [],
                download: false
              })
            )
          )
          return
        }
      }
      timer = setTimeout(() => void tick(), POLL_MS)
    }

    void tick()
  })
}
