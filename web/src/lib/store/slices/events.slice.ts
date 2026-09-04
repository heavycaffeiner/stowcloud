import { createStore, type StoreApi } from 'zustand/vanilla'
import { setAdd, setClear, setDelete } from '../core/fp'

export interface EventsState {
  readonly connected: boolean
  readonly wantedPaths: ReadonlyMap<string, number>
  readonly backoffIndex: number
}

export type EventsAction =
  | { type: 'CONNECTED' }
  | { type: 'DISCONNECTED' }
  | { type: 'INCREMENT_BACKOFF' }
  | { type: 'RESET_BACKOFF' }
  | { type: 'SUBSCRIBE_PATH'; path: string }
  | { type: 'UNSUBSCRIBE_PATH'; path: string }

export function eventsReducer(state: EventsState, action: EventsAction): EventsState {
  switch (action.type) {
    case 'CONNECTED':
      return { ...state, connected: true, backoffIndex: 0 }
    case 'DISCONNECTED':
      return { ...state, connected: false }
    case 'INCREMENT_BACKOFF':
      return { ...state, backoffIndex: Math.min(state.backoffIndex + 1, 6) }
    case 'RESET_BACKOFF':
      return { ...state, backoffIndex: 0 }
    case 'SUBSCRIBE_PATH': {
      const nextMap = new Map(state.wantedPaths)
      const count = nextMap.get(action.path) ?? 0
      nextMap.set(action.path, count + 1)
      return { ...state, wantedPaths: nextMap }
    }
    case 'UNSUBSCRIBE_PATH': {
      const nextMap = new Map(state.wantedPaths)
      const count = nextMap.get(action.path) ?? 0
      if (count <= 1) {
        nextMap.delete(action.path)
      } else {
        nextMap.set(action.path, count - 1)
      }
      return { ...state, wantedPaths: nextMap }
    }
    default:
      return state
  }
}

export interface EventCoalescer {
  notify(path: string): void
  flush(): void
  cancel(): void
  pendingCount(): number
}

/**
 * Coalesces rapid bursts of file invalidation events into a single batched flush.
 *
 * Under multi-user concurrency (e.g. 5 to 1200 users mutating files simultaneously),
 * a WebSocket can deliver hundreds of invalidation frames per second for shared folders.
 * This pipeline buffers path invalidations across a sliding window and triggers one
 * single reconciliation call rather than thrashing the network and DOM.
 */
export function createEventCoalescer(
  onFlush: (paths: ReadonlySet<string>) => void,
  windowMs = 150
): EventCoalescer {
  let pending = new Set<string>()
  let timer: number | null = null

  function flush(): void {
    if (timer !== null) {
      window.clearTimeout(timer)
      timer = null
    }
    if (pending.size === 0) return
    const pathsToDeliver = pending
    pending = new Set<string>()
    onFlush(pathsToDeliver)
  }

  function notify(path: string): void {
    pending.add(path)
    if (timer === null) {
      timer = window.setTimeout(flush, windowMs)
    }
  }

  function cancel(): void {
    if (timer !== null) {
      window.clearTimeout(timer)
      timer = null
    }
    pending.clear()
  }

  return {
    notify,
    flush,
    cancel,
    pendingCount: () => pending.size
  }
}

export interface EventsStore extends StoreApi<EventsState> {
  dispatch(action: EventsAction): void
}

export function createEventsStore(): EventsStore {
  const store = createStore<EventsState>(() => ({
    connected: false,
    wantedPaths: new Map<string, number>(),
    backoffIndex: 0
  }))

  return {
    ...store,
    dispatch(action: EventsAction): void {
      store.setState((prev) => eventsReducer(prev, action))
    }
  }
}
