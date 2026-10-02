import { useEffect, useRef } from 'react'
import { selection } from '../model/selection'
import type { FileEntry } from '../model/file-entry'

export function useFileFocusPreservation(items: readonly FileEntry[], focused: number | null) {
  const focusSnapshot = useRef<{ names: string[]; focusedName: string | null }>({ names: [], focusedName: null })
  const focusedName = focused === null ? null : (items[focused]?.name ?? null)

  useEffect(() => {
    const namesNow = items.map((item) => item.name)
    const previous = focusSnapshot.current
    const overlap = Math.min(previous.names.length, namesNow.length)
    const reordered = overlap > 0 && previous.names.slice(0, overlap).some((name, index) => namesNow[index] !== name)
    if (reordered && previous.focusedName) {
      const next = namesNow.indexOf(previous.focusedName)
      if (next >= 0 && next !== focused) selection.focus(next)
    }
    focusSnapshot.current = {
      names: namesNow,
      focusedName:
        reordered && previous.focusedName && namesNow.includes(previous.focusedName)
          ? previous.focusedName
          : focusedName
    }
  }, [items, focused, focusedName])

  return focusedName
}
