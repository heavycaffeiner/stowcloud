import { useSet } from 'react-simplikit'
import type { TrashEntry } from '../api'

/** The checked trash items. An id the listing no longer holds stops counting as checked. */
export function useTrashSelection(entries: readonly TrashEntry[]) {
  const [checked, { toggle, setAll, reset }] = useSet<string>()
  const selected: ReadonlySet<string> = new Set(entries.map((entry) => entry.id).filter((id) => checked.has(id)))
  return { selected, toggle, setAll, clear: reset }
}

export type TrashSelection = ReturnType<typeof useTrashSelection>
