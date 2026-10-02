// The account's trash: what is in it, putting items back, and removing them
// for good.
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { client, decimal, unwrap } from '../../api/fetcher'
import type { components } from '../../api/generated/schema'
import { keys } from '../../api/query-keys'
import type { BatchItemResult, BatchResult } from '../file-browser/api'

type Schemas = components['schemas']

/** `id` is opaque and goes back to the server verbatim: a trashed item has no
 *  live path to be addressed by. */
export interface TrashEntry {
  id: string
  name: string
  /** A folder's own size is not what it holds, so the screen does not show it. */
  is_dir: boolean
  size: number
  deleted_at_ns: string
}

function trashEntryFromWire(w: Schemas['TrashView']): TrashEntry {
  return {
    id: w.id,
    name: w.name,
    is_dir: w.is_dir,
    size: decimal(w.size, 'trash size'),
    deleted_at_ns: w.deleted_at_ns
  }
}

/** `path` on each result is the item's trash id. */
function batchFromWire(w: Schemas['BatchOutputBody']): BatchResult {
  return {
    results: (w.results ?? []).map((item): BatchItemResult => ({
      path: item.path,
      ok: item.ok,
      error: item.error && {
        code: item.error.code,
        message: item.error.message,
        detail: item.error.reason_key
          ? { reason_key: item.error.reason_key, reason_params: item.error.reason_params }
          : undefined
      }
    }))
  }
}

async function listTrash(): Promise<TrashEntry[]> {
  return ((await unwrap(client.GET('/api/v1/trash'))) ?? []).map(trashEntryFromWire)
}

export function useTrash() {
  return useQuery({ queryKey: keys.trash(), queryFn: listTrash })
}

export function useRestoreTrash() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async (ids: string[]) =>
      batchFromWire(await unwrap(client.POST('/api/v1/trash/restore', { body: { ids } }))),
    // The answer does not say where restored items landed, so every listing
    // is suspect.
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: keys.trash() })
      void queryClient.invalidateQueries({ queryKey: ['path'] })
    }
  })
}

export function usePurgeTrash() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async (ids: string[]) =>
      batchFromWire(await unwrap(client.POST('/api/v1/trash/purge', { body: { ids } }))),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: keys.trash() })
  })
}
