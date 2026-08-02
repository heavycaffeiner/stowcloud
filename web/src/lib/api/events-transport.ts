// web/src/lib/api/events-transport.ts — WebSocket transport for
// `GET /api/events` (DESIGN-API.md §7, `crates/sc-http/src/ws.rs`). Split out
// from `state/events.ts` the same way `upload/transport.ts` is split from
// `state/upload-tray.svelte.ts`: this file only knows how to open a socket
// and shuttle JSON frames. Reconnection policy, which paths are "wanted",
// and what an `inval` should do about it belong one layer up — this never
// reconnects on its own, so the caller's backoff is the only backoff.
import { isMock } from './client'
import type { ClientMsg, ServerMsg } from './types'

export interface EventsTransport {
  /** Opens (or re-opens) the socket. `onMessage` fires per decoded frame;
   *  `onOpen`/`onClose` bracket the connection's lifetime. A frame that
   *  fails to parse is dropped rather than torn down as a connection
   *  error — a malformed frame from a server this client already
   *  authenticated to is a server-side bug, not a reason to lose an
   *  otherwise-healthy socket and everyone's live subscriptions with it. */
  connect(onMessage: (msg: ServerMsg) => void, onOpen: () => void, onClose: () => void): void
  send(msg: ClientMsg): void
  close(): void
}

/** Same-origin `ws`/`wss` URL for `/api/events`, derived from the page's own
 *  origin so this works unmodified behind both plain `http` dev and TLS-
 *  terminating Tailscale `wss` in production, and through `vite dev`'s proxy
 *  (`vite.config.ts` sets `ws: true` for every `/api` path precisely so this
 *  case works too). `VITE_API_BASE` (cross-origin dev against a remote
 *  server) is the one case a same-origin derivation can't cover, so it gets
 *  a direct scheme swap instead. */
function wsUrl(): string {
  const rawBase = (import.meta.env.VITE_API_BASE ?? '') as string
  if (rawBase) {
    return rawBase.replace(/^http/, 'ws') + '/api/events'
  }
  const proto = typeof location !== 'undefined' && location.protocol === 'https:' ? 'wss:' : 'ws:'
  const host = typeof location !== 'undefined' ? location.host : '127.0.0.1'
  return `${proto}//${host}/api/events`
}

class WsEventsTransport implements EventsTransport {
  #ws: WebSocket | null = null

  connect(onMessage: (msg: ServerMsg) => void, onOpen: () => void, onClose: () => void): void {
    const ws = new WebSocket(wsUrl())
    this.#ws = ws
    ws.addEventListener('open', onOpen)
    ws.addEventListener('message', (ev) => {
      try {
        onMessage(JSON.parse(ev.data as string) as ServerMsg)
      } catch {
        // See this class's doc comment: a bad frame is dropped, not fatal.
      }
    })
    ws.addEventListener('close', onClose)
    // A connection-level error is always followed by a `close` event too
    // (part of the WebSocket spec), so `onClose` alone drives the caller's
    // reconnect — a separate `error` listener would just be a second path
    // to the same decision, so this exists only to stop the browser from
    // logging an "uncaught" event for something the `close` handler already
    // deals with.
    ws.addEventListener('error', () => {})
  }

  send(msg: ClientMsg): void {
    if (this.#ws?.readyState === WebSocket.OPEN) this.#ws.send(JSON.stringify(msg))
  }

  close(): void {
    // A close the caller asked for, not the server — drop the listeners
    // first so this doesn't fire the caller's own `onClose` and trigger a
    // reconnect for a socket it just told to go away.
    if (this.#ws) {
      this.#ws.onclose = null
      this.#ws.close()
    }
    this.#ws = null
  }
}

/** `VITE_API_MOCK=1` never starts a real backend (`client.ts`'s own header
 *  comment) and there is no multi-client scenario to simulate within one
 *  browser tab, so this is a deliberate no-op rather than a fake event
 *  source: `onOpen` never fires, `send` is inert, and `EventsHub` built
 *  against it just permanently believes it's still connecting — which is
 *  the correct mock-mode behavior (nothing calls it a live connection that
 *  isn't one). */
class NullEventsTransport implements EventsTransport {
  connect(): void {}
  send(): void {}
  close(): void {}
}

export const eventsTransport: EventsTransport = isMock ? new NullEventsTransport() : new WsEventsTransport()
