// Background jobs: the list the server still knows about, and one live status
// per job the tray is showing.
//
// The poll loop this replaces lived in `state/jobs.ts` and re-implemented
// retry, cancellation and terminal detection by hand. Here the terminal states
// are just where `refetchInterval` returns false.
import { mutationOptions, queryOptions } from '@tanstack/svelte-query'
import { api, type JobState, type JobStatus } from '../api/client'
import { queryClient } from './client'
import { keys } from './keys'

const POLL_MS = 1000

const TERMINAL: readonly JobState[] = ['done', 'error', 'cancelled', 'interrupted']

function isTerminal(status: JobStatus | undefined): boolean {
  return status !== undefined && TERMINAL.includes(status.state)
}

/** Every non-terminal job this account owns. Polled while any of them is
 *  running, so a job started in another tab shows up here too. */
export function jobListQuery() {
  return queryOptions({
    queryKey: keys.jobs(),
    queryFn: () => api.jobList(),
    refetchInterval: (query) => (query.state.data?.jobs.some((job) => !isTerminal(job)) ? POLL_MS : false),
    staleTime: 0
  })
}

/**
 * One job's progress.
 *
 * Kept alive after the job ends: `GET /api/jobs` drops a finished job, but the
 * tray still has to show how it ended until the row is dismissed.
 */
export function jobQuery(id: string) {
  return queryOptions({
    queryKey: keys.job(id),
    queryFn: () => api.jobStatus(id),
    refetchInterval: (query) => (isTerminal(query.state.data) ? false : POLL_MS),
    // A job row that has been GC'd server-side is gone, not worth retrying.
    retry: false,
    gcTime: 60_000
  })
}

export function jobCancelMutation() {
  return mutationOptions({
    mutationFn: (id: string) => api.jobCancel(id),
    // Cancellation lands at the next item boundary, so the resulting state
    // arrives through the poll rather than from this call.
    onSuccess: (_void, id) => queryClient.invalidateQueries({ queryKey: keys.job(id) })
  })
}
