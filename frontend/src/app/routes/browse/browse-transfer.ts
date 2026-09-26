import { ApiError, type BatchResult, type Entry, type OnConflict } from '../../../lib/api/client'
import type { CopyResult } from '../../../lib/api/types'
import { baseName, joinPath } from '../../../lib/api/path-utils'
import { batchErrorKey, describeApiError } from '../../../lib/api/error-text'
import { jobTray } from '../../../lib/store/jobs.store'
import { selection } from '../../../lib/store/selection.store'
import type { BrowseState } from './types'

type Patch = (patch: Partial<BrowseState> | ((state: BrowseState) => Partial<BrowseState>)) => void
type Translate = (key: string, params?: Record<string, string | number>) => string
type Mutation = { mutateAsync: (args: { paths: string[]; dest: string; onConflict: OnConflict }) => Promise<BatchResult | CopyResult> }

export async function runBrowseTransfer(paths: string[], dest: string, kind: 'move' | 'copy', onConflict: OnConflict, mutations: { move: Mutation; copy: Mutation }, patch: Patch, t: Translate, retry: (paths: string[], dest: string, kind: 'move' | 'copy', policy: OnConflict) => void): Promise<void> {
  if (!paths.length) return
  try {
    const result = await (kind === 'move' ? mutations.move : mutations.copy).mutateAsync({ paths, dest, onConflict })
    const jobs = 'jobs' in result ? result.jobs ?? [] : []
    patch({ operation: { kind, results: result.results, jobs } })
    if (jobs.length) jobTray.track(...jobs)
    const conflicts = result.results.filter((item) => item.error?.code === 'fs.conflict')
    if (conflicts.length) {
      const first = conflicts[0]
      const detailPath = first.error?.detail?.path
      const namedPath = typeof detailPath === 'string' ? detailPath : first.destination ?? first.path
      patch({ conflictName: baseName(namedPath), conflictRetry: () => (next: OnConflict) => retry(conflicts.map((item) => item.path), dest, kind, next), conflictOpen: true })
      return
    }
    const failed = result.results.find((item) => !item.ok)
    if (failed) {
      if (failed.error?.code === 'quota.exceeded') patch({ snackbar: kind === 'move' ? t('browse.not_enough_storage_space_move') : t('browse.not_enough_storage_space_copy') })
      else { const key = batchErrorKey(failed.error); patch({ snackbar: key ? t(key.key, key.params) : t(kind === 'move' ? 'browse.move_job_failed' : 'browse.copy_job_failed') }) }
    } else { selection.clear(); patch({ snackbar: t('common.done') }) }
  } catch (error) {
    if (error instanceof ApiError && error.code === 'fs.conflict') patch({ conflictName: baseName(typeof error.detail?.path === 'string' ? error.detail.path : paths[0] ?? ''), conflictRetry: () => (next: OnConflict) => retry(paths, dest, kind, next), conflictOpen: true })
    else if (error instanceof ApiError && error.code === 'quota.exceeded') patch({ snackbar: kind === 'move' ? t('browse.not_enough_storage_space_move') : t('browse.not_enough_storage_space_copy') })
    else patch({ snackbar: describeApiError(error, kind === 'move' ? t('browse.move_job_failed') : t('browse.copy_job_failed')) })
  }
}

export function browseTransferSources(path: string, selected: readonly Entry[], contextEntry: Entry | null): string[] {
  return (selected.length ? selected : contextEntry ? [contextEntry] : []).map((entry) => joinPath(path, entry.name))
}
