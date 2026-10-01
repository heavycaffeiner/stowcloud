import { useEffect } from 'react'
import { selection, useSelectionStore } from '../../../lib/store/selection.store'
import type { TrashEntry } from '../api'

export function useTrashSelection(entries: readonly TrashEntry[]) {
  const selected = useSelectionStore((state) => state.names)

  useEffect(() => {
    const known = new Set(entries.map((entry) => entry.id))
    const next = new Set([...selected].filter((id) => known.has(id)))
    if (next.size !== selected.size) selection.replace(next)
  }, [entries, selected])

  return selected
}
