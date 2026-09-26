import { usePatchState } from '../../../hooks/use-component-state'
import type { AdminShare, ShareBackend } from '../../../lib/api/client'

export interface BackendForm {
  hostPath: string
  s3Endpoint: string
  s3Region: string
  s3Bucket: string
  s3Prefix: string
  s3AccessKey: string
  s3SecretKey: string
  s3PathStyle: boolean
  s3PathStyleTouched: boolean
  vaultContainer: string
  vaultPassword: string
  vaultCreate: boolean
  vaultSizeMiB: string
  vaultPIM: string
}

export function emptyBackendForm(): BackendForm {
  return {
    hostPath: '',
    s3Endpoint: '',
    s3Region: 'us-east-1',
    s3Bucket: '',
    s3Prefix: '',
    s3AccessKey: '',
    s3SecretKey: '',
    s3PathStyle: true,
    s3PathStyleTouched: false,
    vaultContainer: '',
    vaultPassword: '',
    vaultCreate: true,
    vaultSizeMiB: '256',
    vaultPIM: ''
  }
}

export interface SharePickerState {
  mode: 'folder' | 'file'
  start: string
  apply: (path: string) => void
}

export interface ShareManagementState {
  smbNote: string | null
  pathPicker: SharePickerState | null
  addOpen: boolean
  addName: string
  addBackend: ShareBackend
  addForm: BackendForm
  addValidation: string | null
  editTarget: AdminShare | null
  editName: string
  editForm: BackendForm
  editValidation: string | null
  deleteTarget: AdminShare | null
  encEnableTarget: AdminShare | null
  encPassphrase: string
  encPassphraseConfirm: string
  encGenerating: boolean
  encGenerateError: string | null
  encDisableTarget: AdminShare | null
  encDisableError: string | null
  announcement: string
  pathPickerCounter: number
}

const initialShareManagementState: ShareManagementState = {
  smbNote: null,
  pathPicker: null,
  addOpen: false,
  addName: '',
  addBackend: 'local',
  addForm: emptyBackendForm(),
  addValidation: null,
  editTarget: null,
  editName: '',
  editForm: emptyBackendForm(),
  editValidation: null,
  deleteTarget: null,
  encEnableTarget: null,
  encPassphrase: '',
  encPassphraseConfirm: '',
  encGenerating: false,
  encGenerateError: null,
  encDisableTarget: null,
  encDisableError: null,
  announcement: '',
  pathPickerCounter: 0
}

export type ShareManagementPatch = Partial<ShareManagementState> | ((state: ShareManagementState) => Partial<ShareManagementState>)

export function useShareManagementState(): readonly [ShareManagementState, (patch: ShareManagementPatch) => void] {
  return usePatchState(initialShareManagementState)
}
