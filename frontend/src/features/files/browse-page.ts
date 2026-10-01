// The folder page's own state. It starts over whenever the browser moves to another folder.
import { batch, signal } from '@preact/signals-react'
import type { BatchItemResult } from './api'

export type BrowseFilterType = 'all' | 'folders' | 'documents' | 'images' | 'videos' | 'audio' | 'archives'
export type BrowseFilterDate = 'any' | 'today' | '7days' | '30days' | 'this_year'

export interface BrowseOperation {
  readonly kind: 'delete' | 'move' | 'copy'
  readonly results: readonly BatchItemResult[]
  readonly jobs: readonly string[]
}

export const filterType = signal<BrowseFilterType>('all')
export const filterDate = signal<BrowseFilterDate>('any')
export const treeOpen = signal(false)
/** A one-line report on the last action. */
export const notice = signal<string | null>(null)
/** Per-item results of the last batch operation. */
export const operation = signal<BrowseOperation | null>(null)

export function resetBrowsePage(): void {
  batch(() => {
    filterType.value = 'all'
    filterDate.value = 'any'
    treeOpen.value = false
    notice.value = null
    operation.value = null
  })
}
