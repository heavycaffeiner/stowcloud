import { type Dispatch, useReducer } from 'react'
import type { AdminGrant, GrantPermName } from '../../../lib/api/client'
import { mergeState, type StatePatch } from '../../../lib/merge-state'

export interface GrantManagementState {
  expandedIds: Set<number>
  addOpen: boolean
  addShareId: string
  addSubpath: string
  addAllow: Set<GrantPermName>
  addDeny: Set<GrantPermName>
  addInherit: boolean
  addLabel: string
  addValidation: string | null
  editTarget: AdminGrant | null
  editAllow: Set<GrantPermName>
  editDeny: Set<GrantPermName>
  editInherit: boolean
  editLabel: string
  editValidation: string | null
  deleteTarget: AdminGrant | null
}

const initialGrantManagementState: GrantManagementState = {
  expandedIds: new Set(),
  addOpen: false,
  addShareId: '',
  addSubpath: '',
  addAllow: new Set(['read', 'download']),
  addDeny: new Set(),
  addInherit: true,
  addLabel: '',
  addValidation: null,
  editTarget: null,
  editAllow: new Set(),
  editDeny: new Set(),
  editInherit: true,
  editLabel: '',
  editValidation: null,
  deleteTarget: null
}

export function useGrantManagementState(): readonly [GrantManagementState, Dispatch<StatePatch<GrantManagementState>>] {
  return useReducer(mergeState<GrantManagementState>, initialGrantManagementState)
}
