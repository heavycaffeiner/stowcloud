// Deciding whether a failed upload request is worth sending again.
//
// Split out of the worker so the policy can be tested without a Worker realm,
// a network, or a server. The worker owns the timers and the state; this file
// owns the decision.

/** How many times one unit of work may be retried before it is given up on. */
export const MAX_RETRIES = 5

/**
 * Backoff before the nth retry, in milliseconds, with the last value repeating.
 * Jitter is applied on top by `retryDelay`.
 */
const BACKOFF_MS = [1000, 2000, 4000, 8000, 8000]

/** The upper bound on a server-supplied Retry-After, so a hostile or confused
 *  proxy cannot park an upload for an hour. */
const RETRY_AFTER_CEILING_MS = 60_000

export type RetryVerdict =
  | { kind: 'retry'; afterMs: number }
  | { kind: 'shrink' }
  | { kind: 'give-up'; reason: 'session-gone' | 'quota' | 'too-large' | 'conflict' | 'out-of-retries' }

/**
 * Decides what to do about a failed request.
 *
 * `status` is the HTTP status, or 0 when the request never got an answer: a
 * dropped connection, a DNS failure, a proxy closing the socket mid-body. Zero
 * is the common case behind a tunnel and is retryable, since nothing says the
 * server refused anything.
 */
export function classifyFailure(status: number, retriesSoFar: number): RetryVerdict {
  // The session is gone: cancelled, expired, or swept. There is no offset to
  // resume from, so sending the same chunk again cannot succeed.
  if (status === 404 || status === 410) return { kind: 'give-up', reason: 'session-gone' }

  // Out of space. Retrying makes the same demand of the same full disk.
  if (status === 507) return { kind: 'give-up', reason: 'quota' }

  // A proxy refused the body's size. A smaller chunk is a different request,
  // so this is not spent from the retry budget.
  if (status === 413) return { kind: 'shrink' }

  // A state conflict (e.g. duplicate file or offset conflict on non-random-access).
  if (status === 409) return { kind: 'give-up', reason: 'conflict' }

  // Anything the server refused outright, other than the cases above, is a
  // refusal rather than a fault: 400, 401, 403 and their neighbours mean the
  // next identical request is refused identically. 429 is excluded because it
  // means "later", not "no".
  if (status >= 400 && status < 500 && status !== 429 && status !== 408) {
    return { kind: 'give-up', reason: 'out-of-retries' }
  }

  if (retriesSoFar >= MAX_RETRIES) return { kind: 'give-up', reason: 'out-of-retries' }
  return { kind: 'retry', afterMs: retryDelay(retriesSoFar) }
}

/**
 * How long to wait before retry number `retriesSoFar` (0-based).
 *
 * Jittered, because the chunks of one upload fail together when a tunnel drops
 * the connection: an unjittered backoff sends every one of them again at the
 * same instant, which is what dropped them.
 *
 * `retryAfterMs`, when the server supplied one, wins over the schedule but is
 * capped: a 429 is the server asking for a specific delay and guessing a
 * shorter one just earns another.
 */
export function retryDelay(retriesSoFar: number, retryAfterMs?: number): number {
  const base =
    retryAfterMs !== undefined && retryAfterMs > 0
      ? Math.min(retryAfterMs, RETRY_AFTER_CEILING_MS)
      : BACKOFF_MS[Math.min(retriesSoFar, BACKOFF_MS.length - 1)]
  // Full jitter over the interval rather than a fraction of it. A narrow band
  // still lets a burst of simultaneous failures retry as a burst.
  return Math.round(base * (0.5 + Math.random() * 0.5))
}

/**
 * Reads a Retry-After header into milliseconds.
 *
 * The header is either a count of seconds or an HTTP date, and both appear in
 * the wild. Anything unparseable reads as absent so the caller falls back to
 * its own schedule.
 */
export function retryAfterMs(header: string | null): number | undefined {
  if (!header) return undefined
  const trimmed = header.trim()

  const seconds = Number(trimmed)
  if (Number.isFinite(seconds) && seconds >= 0) return seconds * 1000

  const at = Date.parse(trimmed)
  if (Number.isNaN(at)) return undefined
  const delta = at - Date.now()
  return delta > 0 ? delta : 0
}
