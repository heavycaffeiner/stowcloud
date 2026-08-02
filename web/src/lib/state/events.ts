// web/src/lib/state/events.ts — live change notifications (`GET /api/events`,
// DESIGN-API.md §7). Owns the one WebSocket connection the whole app shares:
// reconnect-with-backoff, re-subscribing every "wanted" path after a
// reconnect, and the 30s client ping §7 calls for (Cloudflare's own idle
// timeout is 100s — a connection that never speaks gets dropped by the
// proxy, which then looks identical to a real server crash from here).
//
// Not a `.svelte.ts` — nothing here is read reactively by a component
// (`BrowseState.refresh()`, the actual UI update, already is reactive; this
// just decides *when* to call it), so plain class state is enough.
import { eventsTransport } from '../api/events-transport'
import type { ServerMsg } from '../api/types'
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

type InvalCb = (etag: string) => void
type JobCb = (id: string, done: number, total: number) => void

class EventsHub {
  #wanted = new Map<string, Set<InvalCb>>()
  #jobCbs = new Set<JobCb>()
  #connected = false
  #backoffIdx = 0
  #connectedAt = 0
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
      // Logged out — either mid-connection (a `revoked` push, handled in
      // `#onMessage`) or the session simply expired and the next `sub`/`ping`
      // round-trip 401'd the socket closed. `events_ws` requires a session,
      // so reconnecting now would just repeat the same failure forever; stop
      // for good and let a future `ensureConnected` (post-login) restart.
      this.#started = false
      return
    }
    const delay = BACKOFF_MS[this.#backoffIdx]
    this.#reconnectTimer = setTimeout(() => this.#open(), delay)
  }

  #onMessage(msg: ServerMsg): void {
    switch (msg.t) {
      case 'inval': {
        const cbs = this.#wanted.get(msg.path)
        if (cbs) for (const cb of cbs) cb(msg.etag)
        break
      }
      case 'job':
        for (const cb of this.#jobCbs) cb(msg.id, msg.done, msg.total)
        break
      case 'revoked':
        // DESIGN-API.md §7: "session revoked → immediate logout". `noteUnauthorized`
        // is the same single choke point every 401 already goes through
        // (`api/http.ts`'s `request`) — routing through it here instead of a
        // bespoke logout path keeps "what a dead session looks like" to one
        // answer, not two that can drift apart.
        noteUnauthorized()
        this.close()
        break
      case 'quota':
      case 'pong':
        // Both defined by DESIGN-API.md §7; neither has a UI consumer yet
        // (no quota display, no connection-health indicator in this app).
        // Matched explicitly rather than falling off the end of the switch
        // so the next thing that needs one has an obvious place to add it,
        // instead of rediscovering that the server already sends it.
        break
    }
  }

  /** Watches one directory's changes. `cb` fires with the new `etag`
   *  whenever the server reports that `path` changed; the caller decides
   *  what "changed" means to it. Returns an unsubscribe function — call it
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

  /** Job progress pushes (`{"t":"job",...}`) — `state/jobs.ts`'s poller is
   *  the fallback DESIGN-API.md §6 describes ("progress is also pushed over
   *  the websocket; polling is the fallback"); a connected hub delivers
   *  these without a request round-trip per tick. */
  onJob(cb: JobCb): () => void {
    this.#jobCbs.add(cb)
    return () => this.#jobCbs.delete(cb)
  }

  /** Tears the connection down for good — used on logout and by the
   *  `revoked` handler above. `ensureConnected` after a fresh login starts a
   *  new one. */
  close(): void {
    if (this.#reconnectTimer) clearTimeout(this.#reconnectTimer)
    if (this.#pingTimer) clearInterval(this.#pingTimer)
    this.#reconnectTimer = null
    this.#pingTimer = null
    this.#started = false
    this.#connected = false
    eventsTransport.close()
  }
}

export const events = new EventsHub()
