import { useMemo } from 'react'
import { useRouteStore } from '../../use-route-store'

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
  patch: (patch: Partial<EditState> | ((state: EditState) => Partial<EditState>)) => void
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

function clearSealed(sealedDraft: SealedDraft | null): void {
  sealedDraft?.bytes.fill(0)
}

export function useEditState(): { state: EditState; actions: EditActions } {
  const [state, patch] = useRouteStore(initialEditState)
  const actions = useMemo<EditActions>(() => ({
    patch,
    beginPath: (path) => patch((current) => {
      clearSealed(current.sealedDraft)
      return { loadedPath: path, baselineEtag: null, awaitingBaseline: true, draft: null, sealedDraft: null, languageName: null }
    }),
    setDraft: (draft) => patch({ draft }),
    clearDraftIf: (draft) => patch((current) => current.draft === draft ? { draft: null } : {}),
    setLanguage: (languageName) => patch({ languageName }),
    markBaseline: (baselineEtag) => patch({ baselineEtag, awaitingBaseline: false }),
    sealDraft: (sealedDraft) => patch((current) => {
      clearSealed(current.sealedDraft)
      return { sealedDraft, draft: null }
    }),
    restoreDraft: (draft) => patch((current) => {
      clearSealed(current.sealedDraft)
      return { draft, sealedDraft: null }
    }),
    clearDraft: () => patch((current) => {
      clearSealed(current.sealedDraft)
      return { draft: null, sealedDraft: null }
    }),
    setUnlockRequested: (unlockRequested) => patch({ unlockRequested }),
    completeUnlock: () => patch((current) => ({ unlockRequested: false, sessionRevision: current.sessionRevision + 1 })),
    setConflict: (conflictOpen, conflictWeak = false) => patch({ conflictOpen, conflictWeak }),
    setLeaveDialog: (leaveDialogOpen) => patch({ leaveDialogOpen }),
    setSaveError: (saveError) => patch({ saveError }),
    setSnackbar: (snackbar) => patch({ snackbar })
  }), [patch])
  return { state, actions }
}
