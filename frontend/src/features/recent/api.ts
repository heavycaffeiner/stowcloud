// Files this account wrote through this server lately, newest first.
import { useQuery } from '@tanstack/react-query'
import { client, decimal, oneOf, unwrap } from '../../api/fetcher'
import type { components } from '../../api/generated/schema'
import { keys } from '../../api/query-keys'

export type RecentOp = 'upload' | 'edit' | 'copy' | 'move' | 'restore'

export interface RecentHit {
  /** Navigable virtual path, `{label}/{rest}`. */
  vpath: string
  name: string
  size: number
  mtime_ns: string
  /** When the write happened, which is not `mtime_ns` for a restore or a copy
   *  that kept timestamps. */
  at_ns: string
  op: RecentOp
}

const RECENT_OPS: readonly RecentOp[] = ['upload', 'edit', 'copy', 'move', 'restore']

function recentFromWire(w: components['schemas']['RecentView']): RecentHit {
  return {
    vpath: w.path,
    name: w.name,
    size: decimal(w.size, 'recent file size'),
    mtime_ns: w.mtime_ns,
    at_ns: w.at_ns,
    op: oneOf(w.op, RECENT_OPS, 'recent operation', 'upload')
  }
}

async function listRecent(limit: number): Promise<RecentHit[]> {
  const rows = await unwrap(client.GET('/api/v1/files/recent', { params: { query: { limit: String(limit) } } }))
  return (rows ?? []).map(recentFromWire)
}

export function useRecent(limit: number) {
  return useQuery({ queryKey: [...keys.recent(), limit], queryFn: () => listRecent(limit) })
}
