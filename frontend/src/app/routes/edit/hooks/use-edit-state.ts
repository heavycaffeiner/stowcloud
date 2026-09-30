import { type Dispatch, useEffect, useMemo, useReducer } from 'react'
import { mergeState, type StatePatch } from '../../../../lib/merge-state'

export type SealedDraft = { salt: string; bytes: Uint8Array }

export type EditState = {
  draft: string | null
  sealedDraft: SealedDraft | null
  baselineEtag: string | null
  loadedPath: string | null
  awaitingBaseline: boolean
  unlockRequested: boolean
  conflictOpen: boolean
  conflictWeak: boolean
  leaveDialogOpen: boolean
  saveError: string | null
  snackbar: string | null
  sessionRevision: number
  languageName: string | null
}

export type EditActions = {
  patch: Dispatch<StatePatch<EditState>>
  beginPath: (path: string) => void
  setDraft: (draft: string | null) => void
  clearDraftIf: (draft: string) => void
  setLanguage: (languageName: string | null) => void
  markBaseline: (etag: string) => void
  sealDraft: (sealedDraft: SealedDraft) => void
  restoreDraft: (draft: string) => void
  clearDraft: () => void
  setUnlockRequested: (open: boolean) => void
  completeUnlock: () => void
  setConflict: (open: boolean, weak?: boolean) => void
  setLeaveDialog: (open: boolean) => void
  setSaveError: (message: string | null) => void
  setSnackbar: (message: string | null) => void
}

const initialEditState: EditState = {
  draft: null,
  sealedDraft: null,
  baselineEtag: null,
  loadedPath: null,
  awaitingBaseline: true,
  unlockRequested: true,
  conflictOpen: false,
  conflictWeak: false,
  leaveDialogOpen: false,
  saveError: null,
  snackbar: null,
  sessionRevision: 0,
  languageName: null
}

export function useEditState(): { state: EditState; actions: EditActions } {
  const [state, patch] = useReducer(mergeState<EditState>, initialEditState)
  // A sealed draft holds ciphertext of unsaved text; wipe it once it is replaced or the editor goes away.
  useEffect(() => {
    const sealed = state.sealedDraft
    return () => {
      sealed?.bytes.fill(0)
    }
  }, [state.sealedDraft])
  const actions = useMemo<EditActions>(
    () => ({
      patch,
      beginPath: (path) =>
        patch({
          loadedPath: path,
          baselineEtag: null,
          awaitingBaseline: true,
          draft: null,
          sealedDraft: null,
          languageName: null
        }),
      setDraft: (draft) => patch({ draft }),
      clearDraftIf: (draft) => patch((current) => (current.draft === draft ? { draft: null } : {})),
      setLanguage: (languageName) => patch({ languageName }),
      markBaseline: (baselineEtag) => patch({ baselineEtag, awaitingBaseline: false }),
      sealDraft: (sealedDraft) => patch({ sealedDraft, draft: null }),
      restoreDraft: (draft) => patch({ draft, sealedDraft: null }),
      clearDraft: () => patch({ draft: null, sealedDraft: null }),
      setUnlockRequested: (unlockRequested) => patch({ unlockRequested }),
      completeUnlock: () =>
        patch((current) => ({ unlockRequested: false, sessionRevision: current.sessionRevision + 1 })),
      setConflict: (conflictOpen, conflictWeak = false) => patch({ conflictOpen, conflictWeak }),
      setLeaveDialog: (leaveDialogOpen) => patch({ leaveDialogOpen }),
      setSaveError: (saveError) => patch({ saveError }),
      setSnackbar: (snackbar) => patch({ snackbar })
    }),
    [patch]
  )
  return { state, actions }
}
