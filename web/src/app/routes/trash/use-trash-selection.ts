import { useEffect } from 'react'
import type { TrashEntry } from '../../../lib/api/types'
import { selection } from '../../../lib/store/selection.store'
import { useStore } from '../../../lib/store/use-store'

export function useTrashSelection(entries: readonly TrashEntry[]) {
  const selected = useStore(selection, (state) => state.names)

  useEffect(() => {
    const known = new Set(entries.map((entry) => entry.id))
    const next = new Set([...selected].filter((id) => known.has(id)))
    if (next.size !== selected.size) selection.replace(next)
  }, [entries, selected])

  return selected
}
