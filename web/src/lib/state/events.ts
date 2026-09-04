// Live change notifications (GET /api/events).
// Owns the one WebSocket connection the whole app shares:
// reconnect-with-backoff, re-subscribing every "wanted" path after a reconnect,
// and the 30s client ping calls.
//
// Not a .svelte.ts: nothing here is read reactively by a component.
// BrowseState.refresh(), the actual UI update, is reactive.
import { eventsTransport } from '../api/events-transport'
import type { ServerMsg } from '../api/types'
import { createEventCoalescer, type EventCoalescer } from '../store/slices/events.slice'
import { authState, noteUnauthorized } from './auth.svelte'

const PING_MS = 30_000
// Capped exponential backoff so a dead server gets hammered less over time,
// not more — the failure mode this guards is a reconnect loop tight enough
// to matter (many attempts/sec forever), not "eventually reconnects slowly".
const BACKOFF_MS = [500, 1000, 2000, 4000, 8000, 15000, 30000]
// A connection that stayed open at least this long counts as "was actually
// working" and resets the backoff ladder on its next drop. Without this, one
// good connection after a long outage would leave the *next* transient blip
// waiting the full 30s ladder-max instead of starting back at 500ms.
const CONNECTED_RESET_MS = 5000

// No argument: the frame carries the path that changed and nothing else, and
// a subscriber re-reads that path rather than trusting a token to compare.
type InvalCb = () => void

class EventsHub {
  #wanted = new Map<string, Set<InvalCb>>()
  #connected = false
  #backoffIdx = 0
  #connectedAt = 0
  #coalescer: EventCoalescer = createEventCoalescer((paths) => {
    if (authState.screen !== 'browser') return
    for (const path of paths) {
      const cbs = this.#wanted.get(path)
      if (cbs) for (const cb of cbs) cb()
    }
  }, 100)
  #reconnectTimer: ReturnType<typeof setTimeout> | null = null
  #pingTimer: ReturnType<typeof setInterval> | null = null
  /** True once a connection attempt is outstanding or live — guards against
   *  piling up duplicate sockets when several callers `subscribe` in the
   *  same tick, and against scheduling a second reconnect while one is
   *  already pending. */
  #started = false

  /** Idempotent — every place that might be "the first" to need a
   *  connection (app bootstrap, a fresh login, the first `subscribe` call)
   *  can call this without coordinating with the others. */
  ensureConnected(): void {
    if (this.#started) return
    if (authState.screen !== 'browser') return // nothing to connect to yet — see #open's comment
    this.#started = true
    this.#open()
  }

  #open(): void {
    eventsTransport.connect(
      (msg) => this.#onMessage(msg),
      () => this.#onOpen(),
      () => this.#onClose()
    )
  }

  #onOpen(): void {
    this.#connected = true
    this.#connectedAt = Date.now()
    for (const path of this.#wanted.keys()) eventsTransport.send({ t: 'sub', paths: [path] })
    this.#pingTimer = setInterval(() => eventsTransport.send({ t: 'ping' }), PING_MS)
  }

  #onClose(): void {
    this.#connected = false
    if (this.#pingTimer) {
      clearInterval(this.#pingTimer)
      this.#pingTimer = null
    }
    if (Date.now() - this.#connectedAt >= CONNECTED_RESET_MS) this.#backoffIdx = 0
    else this.#backoffIdx = Math.min(this.#backoffIdx + 1, BACKOFF_MS.length - 1)

    if (authState.screen !== 'browser') {
      // Logged out: either mid-connection (a revoked push) or the session
      // simply expired and the next sub/ping round-trip 401'd the socket closed.
      // events_ws requires a session, so reconnecting now would just repeat the
      // same failure forever: stop for good and let a future ensureConnected
      // (post-login) restart.
      this.#coalescer.cancel()
      this.#started = false
      return
    }
    const delay = BACKOFF_MS[this.#backoffIdx]
    this.#reconnectTimer = setTimeout(() => this.#open(), delay)
  }

  #onMessage(msg: ServerMsg): void {
    switch (msg.t) {
      case 'inval': {
        this.#coalescer.notify(msg.path)
        break
      }
      case 'pong':
        // The keepalive's answer. Nothing reads it: the connection being
        // alive is what it proves, and the transport already knows that.
        break
    }
  }

  /** Watches one directory's changes. `cb` fires with the new `etag`
   *  whenever the server reports that `path` changed; the caller decides
   *  what "changed" means to it. Returns an unsubscribe function: call it
   *  before watching a different path, or the old one keeps refreshing a
   *  directory nobody is looking at anymore. */
  subscribe(path: string, cb: InvalCb): () => void {
    this.ensureConnected()
    let cbs = this.#wanted.get(path)
    if (!cbs) {
      cbs = new Set()
      this.#wanted.set(path, cbs)
      if (this.#connected) eventsTransport.send({ t: 'sub', paths: [path] })
    }
    cbs.add(cb)
    const owner = cbs
    return () => {
      owner.delete(cb)
      if (owner.size === 0 && this.#wanted.get(path) === owner) {
        this.#wanted.delete(path)
        if (this.#connected) eventsTransport.send({ t: 'unsub', paths: [path] })
      }
    }
  }

  /** Tears the connection down for good: used on logout and by the
   *  revoked handler above. ensureConnected after a fresh login starts a
   *  new one. */
  close(): void {
    this.#coalescer.cancel()
    clearTimeout(this.#reconnectTimer!)
    clearInterval(this.#pingTimer!)
    this.#reconnectTimer = null
    this.#pingTimer = null
    this.#started = false
    this.#connected = false
    eventsTransport.close()
  }

  /** Flushes any pending coalesced path invalidations immediately. */
  flush(): void {
    this.#coalescer.flush()
  }
}

export const events = new EventsHub()
