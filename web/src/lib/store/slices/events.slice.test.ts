import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  createEventCoalescer,
  createEventsStore,
  eventsReducer,
  type EventsState
} from './events.slice'

describe('createEventCoalescer multi-user event storm pipeline', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('coalesces rapid burst of 1200 invalidations into a single flush', () => {
    const deliveredBatches: Array<ReadonlySet<string>> = []
    const coalescer = createEventCoalescer((paths) => {
      deliveredBatches.push(paths)
    }, 150)

    // Simulate 1200 concurrent users modifying files in 3 active directories
    for (let user = 0; user < 1200; user++) {
      coalescer.notify('/Shared/TeamA')
      coalescer.notify('/Shared/TeamB')
      coalescer.notify('/Shared/General')
    }

    expect(deliveredBatches.length).toBe(0) // buffered in window
    expect(coalescer.pendingCount()).toBe(3) // 3 distinct paths coalesced

    vi.advanceTimersByTime(150)

    expect(deliveredBatches.length).toBe(1)
    expect(Array.from(deliveredBatches[0]).sort()).toEqual([
      '/Shared/General',
      '/Shared/TeamA',
      '/Shared/TeamB'
    ])
    expect(coalescer.pendingCount()).toBe(0)
  })

  it('supports explicit flush on demand', () => {
    const deliveredBatches: Array<ReadonlySet<string>> = []
    const coalescer = createEventCoalescer((paths) => {
      deliveredBatches.push(paths)
    }, 200)

    coalescer.notify('/Docs/Report.pdf')
    coalescer.flush()

    expect(deliveredBatches.length).toBe(1)
    expect(Array.from(deliveredBatches[0])).toEqual(['Docs/Report.pdf'].map((s) => '/' + s))
  })

  it('cancels pending timers and drops buffered paths', () => {
    const deliveredBatches: Array<ReadonlySet<string>> = []
    const coalescer = createEventCoalescer((paths) => {
      deliveredBatches.push(paths)
    }, 200)

    coalescer.notify('/Folder/A')
    coalescer.cancel()

    vi.advanceTimersByTime(300)
    expect(deliveredBatches.length).toBe(0)
    expect(coalescer.pendingCount()).toBe(0)
  })
})

describe('eventsReducer and store', () => {
  const initial: EventsState = {
    connected: false,
    wantedPaths: new Map<string, number>(),
    backoffIndex: 0
  }

  it('handles connect and disconnect transitions', () => {
    const connected = eventsReducer(initial, { type: 'CONNECTED' })
    expect(connected.connected).toBe(true)
    expect(connected.backoffIndex).toBe(0)

    const disconnected = eventsReducer(connected, { type: 'DISCONNECTED' })
    expect(disconnected.connected).toBe(false)
  })

  it('tracks subscriber reference counts for paths', () => {
    const store = createEventsStore()

    store.dispatch({ type: 'SUBSCRIBE_PATH', path: '/Photos' })
    store.dispatch({ type: 'SUBSCRIBE_PATH', path: '/Photos' })
    expect(store.getState().wantedPaths.get('/Photos')).toBe(2)

    store.dispatch({ type: 'UNSUBSCRIBE_PATH', path: '/Photos' })
    expect(store.getState().wantedPaths.get('/Photos')).toBe(1)

    store.dispatch({ type: 'UNSUBSCRIBE_PATH', path: '/Photos' })
    expect(store.getState().wantedPaths.has('/Photos')).toBe(false)
  })
})
