// A dead session seen from any request clears the old account and asks the
// session again, which is what sends the shell to sign-in.
import { MutationObserver, QueryObserver } from '@tanstack/react-query'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '../../src/api/fetcher'
import { queryClient } from '../../src/api/query-client'
import { lock } from '../../src/lib/crypto/e2ee'

vi.mock('../../src/lib/crypto/e2ee', () => ({ lock: vi.fn() }))

const DEAD = new ApiError(401, { code: 'auth.required', message: 'no' })
const UNAVAILABLE = new ApiError(503, { code: 'unavailable', message: 'no' })

function fail(key: readonly unknown[], error: unknown): Promise<unknown> {
  return queryClient.fetchQuery({ queryKey: key, queryFn: () => Promise.reject(error), retry: false }).catch(() => null)
}

afterEach(() => {
  queryClient.clear()
  vi.mocked(lock).mockClear()
})

describe('noteSessionDeath', () => {
  it('drops account data, locks encryption and asks the session again', async () => {
    const session = vi.fn(async () => ({ user: 'ada' }))
    const unwatch = new QueryObserver(queryClient, { queryKey: ['session'], queryFn: session }).subscribe(() => null)
    await vi.waitFor(() => expect(session).toHaveBeenCalledOnce())
    queryClient.setQueryData(['path', '/home', 'list'], [])

    await fail(['path', '/docs', 'list'], DEAD)

    expect(queryClient.getQueryData(['path', '/home', 'list'])).toBeUndefined()
    expect(lock).toHaveBeenCalledOnce()
    await vi.waitFor(() => expect(session).toHaveBeenCalledTimes(2))
    unwatch()
  })

  it('reacts the same to a failed write', async () => {
    queryClient.setQueryData(['path', '/home', 'list'], [])

    await new MutationObserver(queryClient, { mutationFn: () => Promise.reject(DEAD) }).mutate().catch(() => null)

    expect(queryClient.getQueryData(['path', '/home', 'list'])).toBeUndefined()
    expect(lock).toHaveBeenCalledOnce()
  })

  it('does not make the session query refetch its own refusal', async () => {
    const session = vi.fn(() => Promise.reject(DEAD))
    const unwatch = new QueryObserver(queryClient, { queryKey: ['session'], queryFn: session, retry: false }).subscribe(
      () => null
    )
    await vi.waitFor(() => expect(lock).toHaveBeenCalledOnce())
    await new Promise((resolve) => setTimeout(resolve, 20))

    expect(session).toHaveBeenCalledOnce()
    unwatch()
  })

  it('leaves everything alone when the server is only unreachable', async () => {
    queryClient.setQueryData(['path', '/home', 'list'], [])

    await fail(['path', '/docs', 'list'], UNAVAILABLE)

    expect(queryClient.getQueryData(['path', '/home', 'list'])).toEqual([])
    expect(lock).not.toHaveBeenCalled()
  })
})
