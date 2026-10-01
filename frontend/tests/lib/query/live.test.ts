import { QueryObserver } from '@tanstack/react-query'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { eventsTransport, type ServerMsg } from '../../../src/api/events-transport'
import { invalidateDirs } from '../../../src/features/files/api'
import { queryClient } from '../../../src/lib/query/client'
import { startLiveInvalidation } from '../../../src/lib/query/live'

vi.mock('../../../src/api/events-transport', () => ({
  eventsTransport: { connect: vi.fn(), send: vi.fn(), close: vi.fn() }
}))
vi.mock('../../../src/features/files/api', () => ({ invalidateDirs: vi.fn() }))

const connect = vi.mocked(eventsTransport.connect)
const send = vi.mocked(eventsTransport.send)

/** The callbacks of the most recent connection attempt. */
function socket() {
  const [onMessage, onOpen, onClose] = connect.mock.lastCall!
  return { message: (msg: ServerMsg) => onMessage(msg), open: onOpen, close: onClose }
}

function watch(key: readonly unknown[]): () => void {
  return new QueryObserver(queryClient, { queryKey: key, queryFn: () => [] }).subscribe(() => undefined)
}

let stop: () => void

beforeEach(() => {
  vi.useFakeTimers()
  stop = startLiveInvalidation()
})

afterEach(() => {
  stop()
  queryClient.clear()
  vi.clearAllMocks()
  vi.useRealTimers()
})

describe('startLiveInvalidation', () => {
  it('subscribes only to listings something is showing', () => {
    const unwatch = watch(['path', '/docs', 'list'])
    watch(['path', '/docs/a.txt', 'stat'])
    queryClient.setQueryData(['path', '/old', 'list'], [])

    socket().open()
    expect(send).toHaveBeenCalledWith({ t: 'sub', paths: ['/docs'] })

    unwatch()
    expect(send).toHaveBeenLastCalledWith({ t: 'unsub', paths: ['/docs'] })
  })

  it('folds a burst of changes into one invalidation', () => {
    socket().open()
    socket().message({ t: 'inval', path: '/docs' })
    socket().message({ t: 'inval', path: '/docs' })
    socket().message({ t: 'inval', path: '/home' })
    expect(invalidateDirs).not.toHaveBeenCalled()

    vi.advanceTimersByTime(100)
    expect(invalidateDirs).toHaveBeenCalledOnce()
    expect(vi.mocked(invalidateDirs).mock.lastCall![1]).toEqual(new Set(['/docs', '/home']))
  })

  it('waits longer after each failed attempt, up to 30 seconds', () => {
    const delays: number[] = []
    for (let attempt = 0; attempt < 9; attempt++) {
      const before = connect.mock.calls.length
      socket().close()
      let waited = 0
      while (connect.mock.calls.length === before) {
        vi.advanceTimersByTime(100)
        waited += 100
      }
      delays.push(waited)
    }
    expect(delays).toEqual([500, 1000, 2000, 4000, 8000, 15000, 30000, 30000, 30000])
  })

  it('starts the ladder over once a connection has stayed up', () => {
    socket().close()
    vi.advanceTimersByTime(500)
    socket().close()
    vi.advanceTimersByTime(1000)

    socket().open()
    vi.advanceTimersByTime(5000)
    const before = connect.mock.calls.length
    socket().close()
    vi.advanceTimersByTime(500)
    expect(connect.mock.calls.length).toBe(before + 1)
  })

  it('stops reconnecting once stopped', () => {
    stop()
    socket().close()
    vi.advanceTimersByTime(60_000)
    expect(connect).toHaveBeenCalledOnce()
  })
})
