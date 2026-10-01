import { baseName } from '../../../lib/path-utils'
import { batchErrorKey, describeApiError } from '../../../api/error-text'
import { ApiError } from '../../../api/fetcher'
import { t } from '../../../lib/i18n'
import { jobTray } from '../../jobs/tray-store'
import { askConflictPolicy } from '../ConflictDialog'
import { notice, operation } from '../browse-page'
import { selection } from '../selection'
import type { BatchResult, CopyResult, OnConflict } from '../api'

export type TransferKind = 'move' | 'copy'

export type TransferMutation = {
  mutateAsync: (args: { paths: string[]; dest: string; onConflict: OnConflict }) => Promise<BatchResult | CopyResult>
}

/** Moves or copies `paths` into `dest`. On a name conflict it asks once, then retries only the conflicting items. */
export async function runBrowseTransfer(
  paths: string[],
  dest: string,
  kind: TransferKind,
  onConflict: OnConflict,
  mutation: TransferMutation
): Promise<void> {
  if (!paths.length) return
  const failedText = kind === 'move' ? t('browse.move_job_failed') : t('browse.copy_job_failed')
  const quotaText =
    kind === 'move' ? t('browse.not_enough_storage_space_move') : t('browse.not_enough_storage_space_copy')
  const retry = async (retryPaths: string[], conflictPath: string): Promise<void> => {
    const policy = await askConflictPolicy(baseName(conflictPath))
    if (policy) await runBrowseTransfer(retryPaths, dest, kind, policy, mutation)
  }

  let result: BatchResult | CopyResult
  try {
    result = await mutation.mutateAsync({ paths, dest, onConflict })
  } catch (error) {
    if (error instanceof ApiError && error.code === 'fs.conflict')
      return retry(paths, typeof error.detail?.path === 'string' ? error.detail.path : paths[0])
    notice.value =
      error instanceof ApiError && error.code === 'quota.exceeded' ? quotaText : describeApiError(error, failedText)
    return
  }

  const jobs = 'jobs' in result ? (result.jobs ?? []) : []
  operation.value = { kind, results: result.results, jobs }
  if (jobs.length) jobTray.track(...jobs)
  const conflicts = result.results.filter((item) => item.error?.code === 'fs.conflict')
  if (conflicts.length) {
    const first = conflicts[0]
    const detailPath = first.error?.detail?.path
    return retry(
      conflicts.map((item) => item.path),
      typeof detailPath === 'string' ? detailPath : (first.destination ?? first.path)
    )
  }
  const failed = result.results.find((item) => !item.ok)
  if (!failed) {
    selection.clear()
    notice.value = t('common.done')
    return
  }
  const key = batchErrorKey(failed.error)
  notice.value = failed.error?.code === 'quota.exceeded' ? quotaText : key ? t(key.key, key.params) : failedText
}
