import { useReducer } from 'react'
import { describeApiError } from '../../../api/error-text'
import { tp } from '../../../lib/i18n'
import { mergeState } from '../../../lib/merge-state'
import type { BatchItemResult } from '../../files/api'
import { usePurgeTrash, useRestoreTrash } from '../api'
import type { TrashSelection } from './use-trash-selection'

type Translate = (key: string, params?: Record<string, string | number>) => string

export type TrashState = {
  purgeOpen: boolean
  purgeSingle: string | null
  operation: { kind: 'restore' | 'purge'; results: BatchItemResult[] } | null
  notice: string | null
}

export function useTrashActions(t: Translate, { selected, setAll }: TrashSelection) {
  const restore = useRestoreTrash()
  const purge = usePurgeTrash()
  const [state, setState] = useReducer(mergeState<TrashState>, {
    purgeOpen: false,
    purgeSingle: null,
    operation: null,
    notice: null
  })

  function summarize(results: readonly BatchItemResult[], verb: string): string {
    const failed = results.filter((result) => !result.ok).length
    if (failed === 0) return tp('trash.items', results.length, { verb })
    return t('trash.succeeded_failed', { verb, ok: results.length - failed, failed })
  }

  function applyResults(ids: readonly string[], results: readonly BatchItemResult[]): void {
    const failed = new Set(results.filter((result) => !result.ok).map((result) => result.path))
    setAll([...selected].filter((id) => !ids.includes(id) || failed.has(id)))
  }

  async function restoreItems(ids: string[]): Promise<void> {
    if (ids.length === 0) return
    try {
      const response = await restore.mutateAsync(ids)
      setState({
        operation: { kind: 'restore', results: response.results },
        notice: summarize(response.results, t('trash.restored'))
      })
      applyResults(ids, response.results)
    } catch (error) {
      setState({ notice: describeApiError(error, t('trash.could_not_restore')) })
    }
  }

  function requestPurge(id: string | null): void {
    if (id === null && selected.size === 0) return
    setState({ purgeSingle: id, purgeOpen: true })
  }

  function cancelPurge(): void {
    setState({ purgeOpen: false, purgeSingle: null })
  }

  async function confirmPurge(): Promise<void> {
    const ids = state.purgeSingle === null ? [...selected] : [state.purgeSingle]
    setState({ purgeOpen: false, purgeSingle: null })
    if (ids.length === 0) return
    try {
      const response = await purge.mutateAsync(ids)
      setState({
        operation: { kind: 'purge', results: response.results },
        notice: summarize(response.results, t('trash.deleted_permanently'))
      })
      applyResults(ids, response.results)
    } catch (error) {
      setState({ notice: describeApiError(error, t('trash.could_not_delete_permanently')) })
    }
  }

  return {
    state,
    setState,
    busy: restore.isPending || purge.isPending,
    restorePending: restore.isPending,
    purgePending: purge.isPending,
    restoreItems,
    requestPurge,
    cancelPurge,
    confirmPurge
  }
}
