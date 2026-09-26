import { useEffect } from 'react'
import type { QueryClient } from '@tanstack/react-query'
import { keys } from '../../../lib/query/keys'
import type { ReadFileResponse } from '../../../lib/api/types'
import type { EditActions } from './use-edit-state'

export interface EditBaselineOptions {
  path: string
  loadedPath: string | null
  awaitingBaseline: boolean
  entryPath?: string
  etag?: string
  content?: ReadFileResponse
  unlocked: boolean
  queryClient: QueryClient
  actions: Pick<EditActions, 'beginPath' | 'markBaseline'>
}

export function useEditBaseline({ path, loadedPath, awaitingBaseline, entryPath, etag, content, unlocked, queryClient, actions }: EditBaselineOptions): void {
  useEffect(() => {
    if (path === loadedPath) return
    actions.beginPath(path)
  }, [actions, loadedPath, path])

  useEffect(() => {
    if (!awaitingBaseline || !etag || !content || path !== loadedPath) return
    const expected = queryClient.getQueryData(keys.pathContent(entryPath ?? path, etag, unlocked))
    if (expected !== content) return
    actions.markBaseline(etag)
  }, [actions, awaitingBaseline, content, entryPath, etag, loadedPath, path, queryClient, unlocked])
}
