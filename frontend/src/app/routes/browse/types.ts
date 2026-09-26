import type { Entry, OnConflict, BatchItemResult } from '../../../lib/api/client'
import type { Rect } from '../../../features/files/marquee'

export type OperationKind = 'delete' | 'move' | 'copy'
export type UnlockTarget = { salt: string; verifier: string; retry: () => void }
export type BrowseFilterType = 'all' | 'folders' | 'documents' | 'images' | 'videos' | 'audio' | 'archives'
export type BrowseFilterDate = 'any' | 'today' | '7days' | '30days' | 'this_year'

export type BrowseState = {
  newFolderOpen: boolean
  renameTarget: Entry | null
  deleteOpen: boolean
  destOpen: boolean
  conflictOpen: boolean
  conflictName: string
  contextEntry: Entry | null
  contextMenu: { x: number; y: number } | null
  blankMenu: { x: number; y: number } | null
  menuTrigger: HTMLElement | null
  shareTarget: Entry | null
  snackbar: string | null
  operation: { kind: OperationKind; results: BatchItemResult[]; jobs: string[] } | null
  destSources: string[]
  destCanCopy: boolean
  destCanMove: boolean
  conflictRetry: ((kind: OnConflict) => void) | null
  dragOver: boolean
  newMenuOpen: boolean
  newMenuPosition: { x: number; y: number; align: 'start' | 'end' }
  newMenuTrigger: HTMLElement | null
  overflowOpen: boolean
  overflowPosition: { x: number; y: number }
  overflowTrigger: HTMLElement | null
  sortMenuOpen: boolean
  sortMenuPosition: { x: number; y: number }
  sortMenuTrigger: HTMLElement | null
  filterType: BrowseFilterType
  filterDate: BrowseFilterDate
  typeMenuOpen: boolean
  typeMenuPosition: { x: number; y: number }
  dateMenuOpen: boolean
  dateMenuPosition: { x: number; y: number }
  marqueeRect: Rect | null
  marqueeScroll: { x: number; y: number }
  unlockTarget: UnlockTarget | null
  previewOpen: boolean
  previewIndex: number
  treeOpen: boolean
}

export const initialBrowseState: BrowseState = {
  newFolderOpen: false,
  renameTarget: null,
  deleteOpen: false,
  destOpen: false,
  conflictOpen: false,
  conflictName: '',
  contextEntry: null,
  contextMenu: null,
  blankMenu: null,
  menuTrigger: null,
  shareTarget: null,
  snackbar: null,
  operation: null,
  destSources: [],
  destCanCopy: false,
  destCanMove: false,
  conflictRetry: null,
  dragOver: false,
  newMenuOpen: false,
  newMenuPosition: { x: 0, y: 0, align: 'end' },
  newMenuTrigger: null,
  overflowOpen: false,
  overflowPosition: { x: 0, y: 0 },
  overflowTrigger: null,
  sortMenuOpen: false,
  sortMenuPosition: { x: 0, y: 0 },
  sortMenuTrigger: null,
  filterType: 'all',
  filterDate: 'any',
  typeMenuOpen: false,
  typeMenuPosition: { x: 0, y: 0 },
  dateMenuOpen: false,
  dateMenuPosition: { x: 0, y: 0 },
  marqueeRect: null,
  marqueeScroll: { x: 0, y: 0 },
  unlockTarget: null,
  previewOpen: false,
  previewIndex: -1,
  treeOpen: false,
}
