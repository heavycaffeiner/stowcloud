// Background jobs: the list the server still knows about, and one polled
// status per job the tray shows. Polling stops at a terminal state.
import { useMutation, useQueries, useQuery, useQueryClient } from '@tanstack/react-query'
import { client, decimal, oneOf, unwrap, unwrapEmpty } from '../../api/fetcher'
import type { components } from '../../api/generated/schema'
import { keys } from '../../lib/query/keys'
import type { BatchItemResult } from '../files/api'

export type JobState = 'queued' | 'running' | 'paused' | 'retrying' | 'done' | 'error' | 'cancelled' | 'interrupted'

export type JobKind = 'copy' | 'delete' | 'archive' | 'index_build'

export interface JobStatus {
  id: string
  kind: JobKind
  state: JobState
  done: number
  total: number
  attempt: number
  max_attempts: number
  next_run_ns: string
  /** Per-item outcomes, filled once the job is terminal. */
  results: BatchItemResult[]
  /** Items begun but never finished; non-empty only on an interrupted job. */
  attempting: string[]
  /** Items never reached; with `results` and `attempting` they account for
   *  every path the request named. */
  pending: string[]
}

const JOB_STATES: readonly JobState[] = [
  'queued',
  'running',
  'paused',
  'retrying',
  'done',
  'error',
  'cancelled',
  'interrupted'
]
const JOB_KINDS: readonly JobKind[] = ['copy', 'delete', 'archive', 'index_build']
const TERMINAL: readonly JobState[] = ['done', 'error', 'cancelled', 'interrupted']

/** An item's failure reason as the error code the rest of the app renders. */
const ITEM_CODES: Record<string, string> = {
  denied: 'fs.denied',
  not_found: 'fs.not_found',
  conflict: 'fs.conflict',
  failed: 'internal'
}

const POLL_MS = 1000

function itemFromWire(w: components['schemas']['OperationItemView']): BatchItemResult {
  if (w.ok) return { path: w.path, ok: true }
  const reason = w.reason ?? ''
  return { path: w.path, ok: false, error: { code: ITEM_CODES[reason] ?? reason, message: w.text ?? '' } }
}

export function jobFromWire(w: components['schemas']['OperationView']): JobStatus {
  return {
    id: w.id,
    kind: oneOf(w.kind, JOB_KINDS, 'job kind', 'copy'),
    // An unknown state is reported as a failure: anything non-terminal would
    // poll forever.
    state: w.state === 'failed' ? 'error' : oneOf(w.state, JOB_STATES, 'job state', 'error'),
    done: decimal(w.progress, 'job progress'),
    total: decimal(w.total, 'job total'),
    attempt: decimal(w.attempt ?? '0', 'job attempt'),
    max_attempts: decimal(w.max_attempts ?? '0', 'job max attempts'),
    next_run_ns: w.next_run_ns ?? '0',
    results: (w.results ?? []).map(itemFromWire),
    attempting: w.attempting ?? [],
    pending: w.pending ?? []
  }
}

function isTerminal(status: JobStatus | undefined): boolean {
  return status !== undefined && TERMINAL.includes(status.state)
}

async function listJobs(): Promise<JobStatus[]> {
  return ((await unwrap(client.GET('/api/v1/jobs'))) ?? []).map(jobFromWire)
}

async function jobStatus(id: string): Promise<JobStatus> {
  return jobFromWire(await unwrap(client.GET('/api/v1/jobs/{id}', { params: { path: { id } } })))
}

function jobOptions(id: string) {
  return {
    queryKey: keys.job(id),
    queryFn: () => jobStatus(id),
    refetchInterval: (query: { state: { data: JobStatus | undefined } }) =>
      isTerminal(query.state.data) ? false : POLL_MS,
    // A job the server has collected is gone; asking again will not bring it back.
    retry: false,
    // The list drops a finished job, but the tray shows how it ended until dismissed.
    gcTime: 60_000
  }
}

/** Every job this account owns that has not finished, polled while any runs,
 *  so a job started in another tab shows up too. */
export function useJobList() {
  return useQuery({
    queryKey: keys.jobs(),
    queryFn: listJobs,
    refetchInterval: (query) => (query.state.data?.some((job) => !isTerminal(job)) ? POLL_MS : false),
    staleTime: 0
  })
}

export function useJobStatuses(ids: readonly string[]) {
  return useQueries({ queries: ids.map(jobOptions) })
}

export function useJobStatus(id: string | null) {
  return useQuery({ ...jobOptions(id ?? ''), enabled: id !== null })
}

export type JobAction = 'cancel' | 'retry' | 'pause' | 'resume'

/** The resulting state arrives through the poll, not from this call. */
export function useJobAction(action: JobAction) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => {
      const params = { path: { id } }
      switch (action) {
        case 'cancel':
          return unwrapEmpty(client.POST('/api/v1/jobs/{id}/cancel', { params }))
        case 'retry':
          return unwrapEmpty(client.POST('/api/v1/jobs/{id}/retry', { params }))
        case 'pause':
          return unwrapEmpty(client.POST('/api/v1/jobs/{id}/pause', { params }))
        case 'resume':
          return unwrapEmpty(client.POST('/api/v1/jobs/{id}/resume', { params }))
      }
    },
    onSuccess: (_void, id) => queryClient.invalidateQueries({ queryKey: keys.job(id) })
  })
}
