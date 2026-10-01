// WebSocket transport for
// `GET /api/v1/events` (`backend/internal/files/changes.go`). This file only knows
// how to open a socket and shuttle JSON frames. Reconnection policy, which
// paths are "wanted", and what an `inval` should do about it belong one layer
// up; this never reconnects on its own, so the caller's backoff is the only
// backoff.
import { serverRoot } from '../../api/fetcher'

/** The hub sends the path that changed rather than an etag: a token on the
 *  frame would be one directory read old by the time it arrives. */
export type ServerMsg = { t: 'inval'; path: string } | { t: 'pong' }

export type ClientMsg = { t: 'sub'; paths: string[] } | { t: 'unsub'; paths: string[] } | { t: 'ping' }

/** The frame is untrusted; anything but the two shapes the hub sends is null. */
function serverMsgOf(data: unknown): ServerMsg | null {
  if (typeof data !== 'string') return null
  const value: unknown = JSON.parse(data)
  if (value === null || typeof value !== 'object' || !('t' in value)) return null
  if (value.t === 'pong') return { t: 'pong' }
  if (value.t === 'inval' && 'path' in value && typeof value.path === 'string') return { t: 'inval', path: value.path }
  return null
}

export interface EventsTransport {
  /** Opens (or re-opens) the socket. `onMessage` fires per decoded frame;
   *  `onOpen`/`onClose` bracket the connection's lifetime. A frame that
   *  fails to parse is dropped rather than torn down as a connection
   *  error: a malformed frame from a server this client already
   *  authenticated to is a server-side bug, not a reason to lose an
   *  otherwise-healthy socket and everyone's live subscriptions with it. */
  connect(onMessage: (msg: ServerMsg) => void, onOpen: () => void, onClose: () => void): void
  send(msg: ClientMsg): void
  close(): void
}

/** The page's own origin with the scheme swapped, so TLS in front of the
 *  server carries over; a configured server root gets the same swap. */
function wsUrl(): string {
  if (serverRoot) return serverRoot.replace(/^http/, 'ws') + '/api/v1/events'
  const proto = typeof location !== 'undefined' && location.protocol === 'https:' ? 'wss:' : 'ws:'
  const host = typeof location !== 'undefined' ? location.host : '127.0.0.1'
  return `${proto}//${host}/api/v1/events`
}

class WsEventsTransport implements EventsTransport {
  #ws: WebSocket | null = null

  connect(onMessage: (msg: ServerMsg) => void, onOpen: () => void, onClose: () => void): void {
    const ws = new WebSocket(wsUrl())
    this.#ws = ws
    ws.addEventListener('open', onOpen)
    ws.addEventListener('message', (ev) => {
      let msg: ServerMsg | null = null
      try {
        msg = serverMsgOf(ev.data)
      } catch {
        // A frame that is not JSON is dropped, like any other unreadable one.
      }
      if (msg) onMessage(msg)
    })
    ws.addEventListener('close', onClose)
    // A connection-level error is always followed by a `close` event too
    // (part of the WebSocket spec), so `onClose` alone drives the caller's
    // reconnect: a separate `error` listener would just be a second path
    // to the same decision, so this exists only to stop the browser from
    // logging an "uncaught" event for something the `close` handler already
    // deals with.
    ws.addEventListener('error', () => {})
  }

  send(msg: ClientMsg): void {
    if (this.#ws?.readyState === WebSocket.OPEN) this.#ws.send(JSON.stringify(msg))
  }

  close(): void {
    // A close the caller asked for, not the server: drop the listeners
    // first so this doesn't fire the caller's own `onClose` and trigger a
    // reconnect for a socket it just told to go away.
    if (this.#ws) {
      this.#ws.onclose = null
      this.#ws.close()
    }
    this.#ws = null
  }
}

export const eventsTransport: EventsTransport = new WsEventsTransport()
