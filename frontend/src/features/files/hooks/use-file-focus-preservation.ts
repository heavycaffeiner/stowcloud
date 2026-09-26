import { useEffect, useRef } from 'react'
import type { Entry } from '../../../lib/api/types'
import { selection } from '../../../lib/store/selection.store'

export function useFileFocusPreservation(entries: readonly Entry[], focused: number | null) {
  const focusSnapshot = useRef<{ names: string[]; focusedName: string | null }>({ names: [], focusedName: null })
  const focusedName = focused === null ? null : entries[focused]?.name ?? null

  useEffect(() => {
    const namesNow = entries.map((entry) => entry.name)
    const previous = focusSnapshot.current
    const overlap = Math.min(previous.names.length, namesNow.length)
    const reordered = overlap > 0 && previous.names.slice(0, overlap).some((name, index) => namesNow[index] !== name)
    if (reordered && previous.focusedName) {
      const next = namesNow.indexOf(previous.focusedName)
      if (next >= 0 && next !== focused) selection.focus(next)
    }
    focusSnapshot.current = { names: namesNow, focusedName: reordered && previous.focusedName && namesNow.includes(previous.focusedName) ? previous.focusedName : focusedName }
  }, [entries, focused, focusedName])

  return focusedName
}
