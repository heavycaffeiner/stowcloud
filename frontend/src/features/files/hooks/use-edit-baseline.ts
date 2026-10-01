import { useEffect } from 'react'
import type { EditActions } from './use-edit-state'
import type { FileCache, ReadFileResponse } from '../api'

export interface EditBaselineOptions {
  path: string
  loadedPath: string | null
  awaitingBaseline: boolean
  entryPath?: string
  etag?: string
  content?: ReadFileResponse
  unlocked: boolean
  files: FileCache
  actions: Pick<EditActions, 'beginPath' | 'markBaseline'>
}

export function useEditBaseline({
  path,
  loadedPath,
  awaitingBaseline,
  entryPath,
  etag,
  content,
  unlocked,
  files,
  actions
}: EditBaselineOptions): void {
  useEffect(() => {
    if (path === loadedPath) return
    actions.beginPath(path)
  }, [actions, loadedPath, path])

  useEffect(() => {
    if (!awaitingBaseline || !etag || !content || path !== loadedPath) return
    const expected = files.cachedContent(entryPath ?? path, etag, unlocked)
    if (expected !== content) return
    actions.markBaseline(etag)
  }, [actions, awaitingBaseline, content, entryPath, etag, loadedPath, path, files, unlocked])
}
