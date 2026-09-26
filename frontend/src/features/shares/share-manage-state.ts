import { useCallback, useRef } from 'react'
import { useStore } from 'zustand'
import { createStore, type StoreApi } from 'zustand/vanilla'
import type { ShareLinkInfo } from '../../lib/api/client'

export interface ShareManageState {
  dialogOpen: boolean
  creatingOpen: boolean
  newKind: 'download' | 'drop'
  newRead: boolean
  newDownload: boolean
  newPassword: string
  newExpiry: string
  newExpiryDate: string
  newMaxDownloads: string
  newLabel: string
  createValidation: string | null
  justCreated: ShareLinkInfo | null
  issuedAcknowledged: boolean
  editingId: number | null
  editRead: boolean
  editDownload: boolean
  editClearPassword: boolean
  editNewPassword: string
  editExpiry: string
  editExpiryDate: string
  editMaxDownloads: string
  editLabel: string
  revokeTarget: ShareLinkInfo | null
  copiedId: number | null
  copyErrorId: number | null
}

const initialShareManageState: ShareManageState = {
  dialogOpen: false,
  creatingOpen: false,
  newKind: 'download',
  newRead: true,
  newDownload: true,
  newPassword: '',
  newExpiry: '30d',
  newExpiryDate: '',
  newMaxDownloads: '',
  newLabel: '',
  createValidation: null,
  justCreated: null,
  issuedAcknowledged: false,
  editingId: null,
  editRead: true,
  editDownload: true,
  editClearPassword: false,
  editNewPassword: '',
  editExpiry: 'keep',
  editExpiryDate: '',
  editMaxDownloads: '',
  editLabel: '',
  revokeTarget: null,
  copiedId: null,
  copyErrorId: null
}

export type ShareManagePatch = Partial<ShareManageState> | ((state: ShareManageState) => Partial<ShareManageState>)

export function useShareManageState(): [ShareManageState, (patch: ShareManagePatch) => void] {
  const storeRef = useRef<StoreApi<ShareManageState> | null>(null)
  if (storeRef.current === null) storeRef.current = createStore(() => initialShareManageState)
  const state = useStore(storeRef.current)
  const patch = useCallback((next: ShareManagePatch) => {
    storeRef.current!.setState((current) => ({ ...current, ...(typeof next === 'function' ? next(current) : next) }))
  }, [])
  return [state, patch]
}

export interface UnlockShareState {
  passphrase: string
  unlocking: boolean
  error: string | null
}

const initialUnlockShareState: UnlockShareState = { passphrase: '', unlocking: false, error: null }
export type UnlockSharePatch = Partial<UnlockShareState> | ((state: UnlockShareState) => Partial<UnlockShareState>)

export function useUnlockShareState(): [UnlockShareState, (patch: UnlockSharePatch) => void] {
  const storeRef = useRef<StoreApi<UnlockShareState> | null>(null)
  if (storeRef.current === null) storeRef.current = createStore(() => initialUnlockShareState)
  const state = useStore(storeRef.current)
  const patch = useCallback((next: UnlockSharePatch) => {
    storeRef.current!.setState((current) => ({ ...current, ...(typeof next === 'function' ? next(current) : next) }))
  }, [])
  return [state, patch]
}
