import { useCallback, useRef } from 'react'
import { useStore } from 'zustand'
import { createStore, type StoreApi } from 'zustand/vanilla'
import type { AdminGrant, GrantPermName } from '../../lib/api/client'

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

export type GrantManagementPatch = Partial<GrantManagementState> | ((state: GrantManagementState) => Partial<GrantManagementState>)

export function useGrantManagementState(): readonly [GrantManagementState, (patch: GrantManagementPatch) => void] {
  const storeRef = useRef<StoreApi<GrantManagementState> | null>(null)
  if (storeRef.current === null) storeRef.current = createStore(() => initialGrantManagementState)
  const state = useStore(storeRef.current)
  const patch = useCallback((next: GrantManagementPatch): void => {
    storeRef.current!.setState((current) => typeof next === 'function' ? next(current) : next)
  }, [])
  return [state, patch]
}
