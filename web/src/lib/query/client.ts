// The app's single QueryClient.
//
// A static SPA with `ssr = false` has exactly one client for its whole
// lifetime, so it is a module singleton rather than something threaded through
// context. Components still read it from the provider (`useQueryClient`);
// modules that run outside a component (the WebSocket bridge, the upload
// worker glue) import it directly.
import { MutationCache, QueryCache, QueryClient } from '@tanstack/svelte-query'
import { ApiError, isSessionDead } from '../api/types'
import { lock } from '../crypto/e2ee'
import { invalidateEncryptedShares } from '../crypto/encrypted-shares'
import { keys } from './keys'

/** Retries buy nothing against a refused or malformed request: only a network
 *  fault or a server that is briefly unavailable can answer differently on the
 *  next attempt. */
function retryable(failures: number, error: unknown): boolean {
  if (failures >= 2) return false
  if (!(error instanceof ApiError)) return true // network / abort-adjacent
  return error.status === 0 || error.status === 502 || error.status === 503 || error.status === 504
}

/**
 * Drop state belonging to the authenticated account without removing the
 * session query whose definitive error drives the shell to the login screen.
 * Cancelling first stops in-flight account reads and mutations; the shell's
 * live socket is owned by its auth effect and closes when that effect leaves
 * the browser state.
 */
export function clearAccountState(): void {
  void queryClient.cancelQueries({
    predicate: (query) => query.queryKey[0] !== 'session'
  })
  lock()
  queryClient.removeQueries({
    predicate: (query) => query.queryKey[0] !== 'session'
  })
  invalidateEncryptedShares()
}

/**
 * A dead session, seen from any query or mutation in the app, clears the old
 * account state and re-checks the session itself; its error is what the shell
 * reads to decide between the file browser and the login screen.
 *
 * Skipped only for invalidating the session query's own failure, which is
 * already that answer and would otherwise refetch itself forever.
 */
function noteSessionDeath(error: unknown, key: readonly unknown[] | undefined): void {
  if (!isSessionDead(error)) return
  clearAccountState()
  if (key?.[0] === 'session') return
  void queryClient.invalidateQueries({ queryKey: keys.session() })
}

export const queryClient: QueryClient = new QueryClient({
  queryCache: new QueryCache({
    onError: (error, query) => noteSessionDeath(error, query.queryKey)
  }),
  mutationCache: new MutationCache({
    onError: (error) => noteSessionDeath(error, undefined)
  }),
  defaultOptions: {
    queries: {
      // Long enough that navigating back to a screen is instant, short enough
      // that a change made elsewhere shows up without a reload. Directory
      // listings override it: they have the WebSocket instead.
      staleTime: 30_000,
      gcTime: 5 * 60_000,
      retry: retryable,
      refetchOnWindowFocus: true,
      refetchOnReconnect: true
    },
    mutations: { retry: false }
  }
})
