import { describe, expect, it } from 'vitest'
import type { Entry } from '../../api/types'
import {
  canSave,
  createEditorStore,
  editorReducer,
  isDirty,
  type EditorSnapshot
} from './editor.slice'

const mockEntry: Entry = {
  name: 'file.txt',
  path: '/file.txt',
  kind: 'file',
  size: 50,
  mtime_ns: '1000',
  etag: 'etag-1',
  etag_weak: false,
  perms: { read: true, write: true, create: true, delete: true, rename: true, move: true, share: true, download: true }
}

describe('editorReducer and pure checks', () => {
  const initial: EditorSnapshot = {
    status: 'loading',
    entry: null,
    etag: null,
    originalContent: '',
    content: '',
    isSaving: false,
    errorMessage: null,
    conflict: null
  }

  it('handles load success transition', () => {
    const ready = editorReducer(initial, {
      type: 'LOAD_SUCCESS',
      entry: mockEntry,
      content: 'hello world'
    })

    expect(ready.status).toBe('ready')
    expect(ready.content).toBe('hello world')
    expect(isDirty(ready)).toBe(false)
    expect(canSave(ready)).toBe(false)
  })

  it('tracks dirty and canSave when content changes', () => {
    const ready = editorReducer(initial, {
      type: 'LOAD_SUCCESS',
      entry: mockEntry,
      content: 'hello'
    })

    const edited = editorReducer(ready, { type: 'SET_CONTENT', content: 'hello edited' })
    expect(isDirty(edited)).toBe(true)
    expect(canSave(edited)).toBe(true)

    const readonlyEntry: Entry = { ...mockEntry, perms: { ...mockEntry.perms, write: false } }
    const readonlyState = editorReducer(initial, {
      type: 'LOAD_SUCCESS',
      entry: readonlyEntry,
      content: 'hello'
    })
    const editedReadonly = editorReducer(readonlyState, { type: 'SET_CONTENT', content: 'hello edited' })
    expect(canSave(editedReadonly)).toBe(false)
  })

  it('records conflicts and clears them', () => {
    const ready = editorReducer(initial, {
      type: 'LOAD_SUCCESS',
      entry: mockEntry,
      content: 'text'
    })

    const conflicted = editorReducer(ready, {
      type: 'SAVE_CONFLICT',
      currentEtag: 'new-server-etag',
      isWeak: false
    })

    expect(conflicted.conflict?.currentEtag).toBe('new-server-etag')
    expect(conflicted.isSaving).toBe(false)

    const dismissed = editorReducer(conflicted, { type: 'DISMISS_CONFLICT' })
    expect(dismissed.conflict).toBeNull()
  })
})

describe('createEditorStore', () => {
  it('dispatches actions and updates snapshot', () => {
    const store = createEditorStore()
    expect(store.getState().status).toBe('loading')

    store.dispatch({ type: 'LOAD_SUCCESS', entry: mockEntry, content: 'init' })
    expect(store.getState().status).toBe('ready')
    expect(store.getState().content).toBe('init')
  })
})
