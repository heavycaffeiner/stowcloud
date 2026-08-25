// web/src/lib/state/events.test.ts — EventsHub's subscribe/unsubscribe
// bookkeeping, message routing, and reconnect-with-backoff, independent of a real WebSocket: `../api/events-transport` is
// replaced with a fake whose `connect`/`send`/`close` this file drives by
// hand and inspects.
//
// `events` is a module-level singleton (one connection for the whole app —
// see `events.ts`'s own header comment), so each test gets a fresh module
// instance via `vi.resetModules()` + a dynamic re-import, the same pattern
// `api/setup.test.ts` uses for the same reason.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { SessionInfo, ServerMsg, ClientMsg } from '../api/types'

const fakeSession: SessionInfo = {
  user: { id: 1, name: 'demo', display_name: 'demo', is_admin: true, totp_enabled: false, smb_opt_out: false, smb_enabled: true },
  roots: [],
  csrf: 'csrf-token',
  limits: { chunk_size: 1, chunk_min: 1, max_file_size: null, parallel: 1 },
  features: { webdav: true, smb: false, preview: true, trash: true, shares: true, search: 'name' },
  oidc: { linked: false }
}

interface FakeHandlers {
  onMessage: (msg: ServerMsg) => void
  onOpen: () => void
  onClose: () => void
}

vi.mock('../api/events-transport', () => {
  const state: { handlers: FakeHandlers | null; sent: ClientMsg[]; connectCount: number; closeCount: number } = {
    handlers: null,
    sent: [],
    connectCount: 0,
    closeCount: 0
  }
  const eventsTransport = {
    connect: (onMessage: FakeHandlers['onMessage'], onOpen: FakeHandlers['onOpen'], onClose: FakeHandlers['onClose']) => {
      state.handlers = { onMessage, onOpen, onClose }
      state.connectCount++
    },
    send: (msg: ClientMsg) => state.sent.push(msg),
    close: () => state.closeCount++
  }
  return { eventsTransport, __fakeState: state }
})

let events: typeof import('./events').events
let authSvelte: typeof import('./auth.svelte')
let fake: {
  handlers: FakeHandlers | null
  sent: ClientMsg[]
  connectCount: number
  closeCount: number
}

function open(): void {
  fake.handlers?.onOpen()
}

beforeEach(async () => {
  vi.resetModules()
  vi.useFakeTimers()
  const transportMod = (await import('../api/events-transport')) as unknown as { __fakeState: typeof fake }
  fake = transportMod.__fakeState
  // `vi.mock`'s factory runs once, hoisted — `vi.resetModules()` gives
  // `events.ts` a fresh `EventsHub` every test (asserted throughout this
  // file), but the mock transport it talks to is the same long-lived
  // object, so its call counters have to be zeroed by hand or they
  // accumulate across every test in this file.
  fake.handlers = null
  fake.sent.length = 0
  fake.connectCount = 0
  fake.closeCount = 0
  authSvelte = await import('./auth.svelte')
  ;({ events } = await import('./events'))
  authSvelte.setAuthenticated(fakeSession)
})

afterEach(() => {
  // Cancels any pending reconnect/ping timer this test's hub scheduled —
  // without this, a timer left running past the end of a test (e.g. "waits
  // out the backoff step") can still fire during a *later* test's fake-timer
  // advances and mutate the shared mock transport's call counts out from
  // under it (`__fakeState` is one singleton for the whole file — `vi.mock`
  // factories aren't re-run by `vi.resetModules()` — while `events` itself
  // gets a fresh instance every test).
  events.close()
  vi.useRealTimers()
})

describe('EventsHub.subscribe', () => {
  it('connects lazily and sends `sub` once the socket is open', () => {
    const cb = vi.fn()
    events.subscribe('/Documents', cb)
    expect(fake.connectCount).toBe(1)
    expect(fake.sent).toEqual([]) // not open yet
    open()
    expect(fake.sent).toEqual([{ t: 'sub', paths: ['/Documents'] }])
  })

  it('a second subscriber to the same path does not re-send `sub`', () => {
    open()
    events.subscribe('/Documents', vi.fn())
    fake.sent.length = 0
    events.subscribe('/Documents', vi.fn())
    expect(fake.sent).toEqual([])
  })

  it('unsubscribing the last watcher of a path sends `unsub`; an earlier one does not', () => {
    open()
    const unsubA = events.subscribe('/Documents', vi.fn())
    events.subscribe('/Documents', vi.fn())
    unsubA()
    expect(fake.sent.some((m) => m.t === 'unsub')).toBe(false)
  })

  it('delivers `inval` only to callbacks watching that exact path', () => {
    open()
    const docs = vi.fn()
    const photos = vi.fn()
    events.subscribe('/Documents', docs)
    events.subscribe('/Photos', photos)
    fake.handlers?.onMessage({ t: 'inval', path: '/Documents' })
    expect(docs).toHaveBeenCalled()
    expect(photos).not.toHaveBeenCalled()
  })

  it('re-sends `sub` for every still-wanted path after a reconnect', () => {
    open()
    events.subscribe('/Documents', vi.fn())
    fake.handlers?.onClose()
    // This connection didn't stay open long enough to reset the backoff
    // ladder (`CONNECTED_RESET_MS`), so the next attempt waits the second
    // step (1000ms), not the first.
    vi.advanceTimersByTime(1000)
    fake.sent.length = 0
    open()
    expect(fake.sent).toEqual([{ t: 'sub', paths: ['/Documents'] }])
  })
})

describe('EventsHub reconnect backoff', () => {
  it('does not reconnect immediately after a drop — waits at least the first backoff step', () => {
    events.subscribe('/x', vi.fn())
    const before = fake.connectCount
    fake.handlers?.onClose()
    expect(fake.connectCount).toBe(before) // no synchronous reconnect
    vi.advanceTimersByTime(400)
    expect(fake.connectCount).toBe(before) // still waiting out the 500ms step
    vi.advanceTimersByTime(200)
    expect(fake.connectCount).toBe(before + 1)
  })

  it('never reconnects after the session goes anonymous (no tight loop against a dead session)', () => {
    events.subscribe('/x', vi.fn())
    authSvelte.setAnonymous()
    fake.handlers?.onClose()
    vi.advanceTimersByTime(60_000)
    expect(fake.connectCount).toBe(1) // only the original connect — never retried
  })
})

describe('EventsHub message routing', () => {
  it('forwards an `inval` to the subscriber watching that path', () => {
    events.ensureConnected()
    open()
    const cb = vi.fn()
    events.subscribe('/x', cb)
    fake.handlers?.onMessage({ t: 'inval', path: '/x' })
    expect(cb).toHaveBeenCalled()
  })
})
