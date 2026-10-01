import { useCallback } from 'react'
import { describeApiError } from '../../../api/error-text'
import type { EditActions } from './use-edit-state'
import type { Entry, FileCache } from '../api'

type SaveMutation = {
  isPending: boolean
  mutateAsync: (variables: { path: string; content: string }) => Promise<Entry>
}

export interface EditSaveOptions {
  path: string
  entry: Entry | null
  unlocked: boolean
  canSave: boolean
  content: string
  baselineEtag: string | null
  files: FileCache
  mutation: SaveMutation
  actions: Pick<
    EditActions,
    'setSaveError' | 'setConflict' | 'markBaseline' | 'clearDraftIf' | 'setSnackbar' | 'setLeaveDialog' | 'clearDraft'
  >
  focus: () => void
  translate: (key: string) => string
}

export interface EditSaveFlow {
  save: () => Promise<boolean>
  overwriteAfterConflict: () => Promise<void>
  reloadAfterConflict: () => Promise<void>
  discardAndLeave: () => void
  saveAndLeave: () => Promise<void>
}

type Blocker = { state: string; proceed?: () => void }

export function useEditSave(
  {
    path,
    entry,
    unlocked,
    canSave,
    content,
    baselineEtag,
    files,
    mutation,
    actions,
    focus,
    translate
  }: EditSaveOptions,
  blocker: Blocker
): EditSaveFlow {
  const save = useCallback(async (): Promise<boolean> => {
    if (!canSave || !entry) return false
    actions.setSaveError(null)
    try {
      const latest = await files.fetchStat(path, true)
      if (baselineEtag !== null && latest.etag !== baselineEtag) {
        actions.setConflict(true, latest.etag_weak)
        return false
      }
      const savedContent = content
      const updated = await mutation.mutateAsync({ path, content: savedContent })
      files.rememberSaved(path, updated, savedContent)
      actions.markBaseline(updated.etag)
      actions.clearDraftIf(savedContent)
      actions.setSnackbar(translate(/* i18n */ 'common.saved'))
      focus()
      return true
    } catch (error) {
      actions.setSaveError(describeApiError(error, translate('common.could_not_save')))
      return false
    }
  }, [actions, baselineEtag, canSave, content, focus, mutation, path, files, translate, entry])

  const overwriteAfterConflict = useCallback(async (): Promise<void> => {
    actions.setConflict(false)
    if (!entry) return
    try {
      const savedContent = content
      const updated = await mutation.mutateAsync({ path, content: savedContent })
      files.rememberSaved(path, updated, savedContent)
      actions.markBaseline(updated.etag)
      actions.clearDraftIf(savedContent)
      actions.setSnackbar(translate(/* i18n */ 'editor.overwritten'))
      focus()
      if (blocker.state === 'blocked') blocker.proceed?.()
    } catch (error) {
      actions.setSnackbar(describeApiError(error, translate('common.could_not_save')))
    }
  }, [actions, blocker, content, entry, focus, mutation, path, files, translate])

  const reloadAfterConflict = useCallback(async (): Promise<void> => {
    actions.setConflict(false)
    try {
      const freshMeta = await files.fetchStat(path, true)
      await files.fetchContent(freshMeta, unlocked)
      actions.markBaseline(freshMeta.etag)
      actions.clearDraft()
      actions.setSnackbar(translate(/* i18n */ 'editor.reloaded_newer_version'))
      focus()
    } catch (error) {
      actions.setSnackbar(describeApiError(error, translate('editor.could_not_load_file')))
    }
  }, [actions, focus, path, files, translate, unlocked])

  const discardAndLeave = useCallback(() => {
    actions.clearDraft()
    actions.setLeaveDialog(false)
    if (blocker.state === 'blocked') blocker.proceed?.()
  }, [actions, blocker])

  const saveAndLeave = useCallback(async () => {
    if ((await save()) && blocker.state === 'blocked') {
      actions.setLeaveDialog(false)
      blocker.proceed?.()
    }
  }, [actions, blocker, save])

  return { save, overwriteAfterConflict, reloadAfterConflict, discardAndLeave, saveAndLeave }
}
