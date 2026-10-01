// The folder page's own state. It starts over whenever the browser moves to another folder.
import { batch, signal } from '@preact/signals-react'
import type { BatchItemResult } from './api'

export interface BrowseOperation {
  readonly kind: 'delete' | 'move' | 'copy'
  readonly results: readonly BatchItemResult[]
  readonly jobs: readonly string[]
}

export const treeOpen = signal(false)
/** A one-line report on the last action. */
export const notice = signal<string | null>(null)
/** Per-item results of the last batch operation. */
export const operation = signal<BrowseOperation | null>(null)

export function resetBrowsePage(): void {
  batch(() => {
    treeOpen.value = false
    notice.value = null
    operation.value = null
  })
}
