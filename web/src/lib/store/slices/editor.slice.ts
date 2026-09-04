import { createStore, type StoreApi } from 'zustand/vanilla'
import type { Entry } from '../../api/types'

export type EditorStatus = 'loading' | 'ready' | 'error'

export interface ConflictInfo {
  readonly currentEtag: string
  readonly isWeak: boolean
}

export interface EditorSnapshot {
  readonly status: EditorStatus
  readonly entry: Entry | null
  readonly etag: string | null
  readonly originalContent: string
  readonly content: string
  readonly isSaving: boolean
  readonly errorMessage: string | null
  readonly conflict: ConflictInfo | null
}

export type EditorAction =
  | { type: 'LOAD_START' }
  | { type: 'LOAD_SUCCESS'; entry: Entry; content: string }
  | { type: 'LOAD_ERROR'; message: string }
  | { type: 'SET_CONTENT'; content: string }
  | { type: 'SAVE_START' }
  | { type: 'SAVE_SUCCESS'; updated: Entry; content: string }
  | { type: 'SAVE_CONFLICT'; currentEtag: string; isWeak: boolean }
  | { type: 'SAVE_ERROR'; message: string }
  | { type: 'DISMISS_CONFLICT' }

export function isDirty(state: EditorSnapshot): boolean {
  return state.status === 'ready' && state.content !== state.originalContent
}

export function canSave(state: EditorSnapshot): boolean {
  if (state.status !== 'ready' || state.isSaving || !isDirty(state)) return false
  return state.entry ? state.entry.perms.write : false
}

export function editorReducer(state: EditorSnapshot, action: EditorAction): EditorSnapshot {
  switch (action.type) {
    case 'LOAD_START':
      return {
        ...state,
        status: 'loading',
        entry: null,
        etag: null,
        originalContent: '',
        content: '',
        errorMessage: null,
        conflict: null
      }
    case 'LOAD_SUCCESS':
      return {
        ...state,
        status: 'ready',
        entry: action.entry,
        etag: action.entry.etag,
        originalContent: action.content,
        content: action.content,
        errorMessage: null
      }
    case 'LOAD_ERROR':
      return {
        ...state,
        status: 'error',
        errorMessage: action.message,
        entry: null
      }
    case 'SET_CONTENT':
      return {
        ...state,
        content: action.content
      }
    case 'SAVE_START':
      return {
        ...state,
        isSaving: true,
        errorMessage: null
      }
    case 'SAVE_SUCCESS':
      return {
        ...state,
        isSaving: false,
        entry: action.updated,
        etag: action.updated.etag,
        originalContent: action.content,
        content: action.content,
        errorMessage: null
      }
    case 'SAVE_CONFLICT':
      return {
        ...state,
        isSaving: false,
        conflict: { currentEtag: action.currentEtag, isWeak: action.isWeak }
      }
    case 'SAVE_ERROR':
      return {
        ...state,
        isSaving: false,
        errorMessage: action.message
      }
    case 'DISMISS_CONFLICT':
      return {
        ...state,
        conflict: null
      }
    default:
      return state
  }
}

export interface EditorStore extends StoreApi<EditorSnapshot> {
  dispatch(action: EditorAction): void
}

export function createEditorStore(): EditorStore {
  const store = createStore<EditorSnapshot>(() => ({
    status: 'loading',
    entry: null,
    etag: null,
    originalContent: '',
    content: '',
    isSaving: false,
    errorMessage: null,
    conflict: null
  }))

  return {
    ...store,
    dispatch(action: EditorAction): void {
      store.setState((prev) => editorReducer(prev, action))
    }
  }
}
