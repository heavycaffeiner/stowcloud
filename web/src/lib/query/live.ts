// Live change notifications wired straight into the query cache.
//
// Nothing subscribes by hand any more: the directories worth watching are the
// ones something is currently observing a listing for, read off the cache, and
// an `inval` frame is an `invalidateQueries` for that path. A change made over
// SMB, by a sync client or in another tab therefore lands on screen through
// the same path as a change made here.
import { eventsTransport } from '../api/events-transport'
import type { ServerMsg } from '../api/types'
import { queryClient } from './client'
import { invalidateDirs } from './files'

const PING_MS = 30_000
/** Capped exponential backoff: a dead server gets hammered less over time,
 *  not more. */
const BACKOFF_MS = [500, 1000, 2000, 4000, 8000, 15000, 30000]
/** A connection that stayed open this long was working, so its next drop
 *  starts the ladder over instead of inheriting an old outage's position. */
const CONNECTED_RESET_MS = 5000
/** A shared folder under load delivers hundreds of frames a second. One
 *  invalidation pass per window beats one per frame. */
const COALESCE_MS = 100

/** Directories something is currently rendering a listing for. Only a listing
 *  counts: a `stat` or a size is read about a path, not watched for changes. */
function observedPaths(): Set<string> {
  const paths = new Set<string>()
  for (const query of queryClient.getQueryCache().getAll()) {
    const [kind, path, part] = query.queryKey
    if (kind === 'path' && part === 'list' && typeof path === 'string' && query.getObserversCount() > 0) {
      paths.add(path)
    }
  }
  return paths
}

/**
 * Opens the connection and keeps it in step with what is on screen until the
 * returned function is called.
 *
 * Called once from the authenticated shell, since the socket needs the same
 * session cookie the rest of the API does.
 */
export function startLiveInvalidation(): () => void {
  let stopped = false
  let connected = false
  let backoffIndex = 0
  let connectedAt = 0
  // Browser timer handles are never 0, so 0 reads as "not scheduled" and
  // every clear below can be unconditional.
  let reconnectTimer = 0
  let pingTimer = 0
  let flushTimer = 0
  let pending = new Set<string>()
  let subscribed = new Set<string>()

  function flush(): void {
    flushTimer = 0
    if (pending.size === 0) return
    const paths = pending
    pending = new Set<string>()
    invalidateDirs(paths)
  }

  function syncSubscriptions(): void {
    if (!connected) return
    const wanted = observedPaths()
    const added = [...wanted].filter((path) => !subscribed.has(path))
    const removed = [...subscribed].filter((path) => !wanted.has(path))
    if (added.length > 0) eventsTransport.send({ t: 'sub', paths: added })
    if (removed.length > 0) eventsTransport.send({ t: 'unsub', paths: removed })
    subscribed = wanted
  }

  function onMessage(msg: ServerMsg): void {
    if (msg.t !== 'inval') return
    pending.add(msg.path)
    if (flushTimer === 0) flushTimer = window.setTimeout(flush, COALESCE_MS)
  }

  function onOpen(): void {
    connected = true
    connectedAt = Date.now()
    subscribed = new Set<string>()
    syncSubscriptions()
    pingTimer = window.setInterval(() => eventsTransport.send({ t: 'ping' }), PING_MS)
  }

  function onClose(): void {
    connected = false
    window.clearInterval(pingTimer)
    pingTimer = 0
    if (stopped) return
    backoffIndex = Date.now() - connectedAt >= CONNECTED_RESET_MS ? 0 : Math.min(backoffIndex + 1, BACKOFF_MS.length - 1)
    reconnectTimer = window.setTimeout(open, BACKOFF_MS[backoffIndex])
  }

  function open(): void {
    eventsTransport.connect(onMessage, onOpen, onClose)
  }

  // The cache is the subscription list: a screen that starts observing a
  // directory subscribes to it, and one that stops unsubscribes.
  const unwatchCache = queryClient.getQueryCache().subscribe(syncSubscriptions)
  open()

  return () => {
    stopped = true
    unwatchCache()
    window.clearTimeout(reconnectTimer)
    window.clearInterval(pingTimer)
    window.clearTimeout(flushTimer)
    pending.clear()
    subscribed.clear()
    eventsTransport.close()
  }
}
