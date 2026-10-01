import { type Dispatch, useReducer } from 'react'
import { mergeState, type StatePatch } from '../../../lib/merge-state'
import type { ShareLinkInfo } from '../../links/api'

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

export function useShareManageState(): [ShareManageState, Dispatch<StatePatch<ShareManageState>>] {
  return useReducer(mergeState<ShareManageState>, initialShareManageState)
}

export interface UnlockShareState {
  passphrase: string
  unlocking: boolean
  error: string | null
}

const initialUnlockShareState: UnlockShareState = { passphrase: '', unlocking: false, error: null }
export function useUnlockShareState(): [UnlockShareState, Dispatch<StatePatch<UnlockShareState>>] {
  return useReducer(mergeState<UnlockShareState>, initialUnlockShareState)
}
