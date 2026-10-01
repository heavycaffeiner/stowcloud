import { useState } from 'react'
import { describeApiError } from '../../../api/error-text'
import { t, tp } from '../../../lib/i18n'
import type { BatchItemResult } from '../../files/api'
import { usePurgeTrash, useRestoreTrash } from '../api'
import { confirmPurge } from '../routes/TrashView'
import type { TrashSelection } from './use-trash-selection'

export interface TrashOperationResult {
  readonly kind: 'restore' | 'purge'
  readonly results: readonly BatchItemResult[]
}

function summarize(results: readonly BatchItemResult[], verb: string): string {
  const failed = results.filter((result) => !result.ok).length
  if (failed === 0) return tp('trash.items', results.length, { verb })
  return t('trash.succeeded_failed', { verb, ok: results.length - failed, failed })
}

export function useTrashActions({ selected, setAll }: TrashSelection) {
  const restore = useRestoreTrash()
  const purge = usePurgeTrash()
  const [operation, setOperation] = useState<TrashOperationResult | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  // The row whose purge is being confirmed stays rendered, so focus can go back to its button.
  const [pinned, setPinned] = useState<string | null>(null)

  /** Reports the results, and unchecks the items that went through. */
  const settle = (kind: TrashOperationResult['kind'], ids: readonly string[], results: readonly BatchItemResult[]) => {
    setOperation({ kind, results })
    setNotice(summarize(results, kind === 'restore' ? t('trash.restored') : t('trash.deleted_permanently')))
    const failed = new Set(results.filter((result) => !result.ok).map((result) => result.path))
    setAll([...selected].filter((id) => !ids.includes(id) || failed.has(id)))
  }

  async function restoreItems(ids: string[]): Promise<void> {
    if (ids.length === 0) return
    try {
      settle('restore', ids, (await restore.mutateAsync(ids)).results)
    } catch (error) {
      setNotice(describeApiError(error, t('trash.could_not_restore')))
    }
  }

  /** Purges one item, or the checked ones when `id` is null, once the user confirms. */
  async function purgeItems(id: string | null): Promise<void> {
    const ids = id === null ? [...selected] : [id]
    if (ids.length === 0) return
    setPinned(id)
    const confirmed = await confirmPurge(ids.length)
    setPinned(null)
    if (!confirmed) return
    try {
      settle('purge', ids, (await purge.mutateAsync(ids)).results)
    } catch (error) {
      setNotice(describeApiError(error, t('trash.could_not_delete_permanently')))
    }
  }

  return {
    operation,
    notice,
    pinned,
    busy: restore.isPending || purge.isPending,
    restorePending: restore.isPending,
    purgePending: purge.isPending,
    restoreItems,
    purgeItems,
    dismissOperation: () => setOperation(null),
    dismissNotice: () => setNotice(null)
  }
}
