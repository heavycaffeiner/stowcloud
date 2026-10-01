import { type Dispatch, useReducer } from 'react'
import { mergeState, type StatePatch } from '../../../lib/merge-state'
import type { ApplyOutcome } from '../api'

export type ServerSettingsGroup =
  'smb' | 'search' | 'thumbnail' | 'archive' | 'network' | 'db' | 'homes' | 'watch' | 'rate' | 'oidc'
export type ServerSettingsValues = Record<string, unknown>

export interface ServerSettingsState {
  values: ServerSettingsValues
  baselines: Record<ServerSettingsGroup, string | null>
  hydrated: Record<ServerSettingsGroup, boolean>
  activeGroup: ServerSettingsGroup | null
  validationError: string | null
  outcome: ApplyOutcome | null
  secret: string
  announcement: string
}

export const SERVER_SETTINGS_GROUPS: readonly ServerSettingsGroup[] = [
  'smb',
  'search',
  'thumbnail',
  'archive',
  'network',
  'db',
  'homes',
  'watch',
  'rate',
  'oidc'
]

const initialServerSettingsState: ServerSettingsState = {
  values: {},
  baselines: Object.fromEntries(SERVER_SETTINGS_GROUPS.map((group) => [group, null])) as Record<
    ServerSettingsGroup,
    string | null
  >,
  hydrated: Object.fromEntries(SERVER_SETTINGS_GROUPS.map((group) => [group, false])) as Record<
    ServerSettingsGroup,
    boolean
  >,
  activeGroup: null,
  validationError: null,
  outcome: null,
  secret: '',
  announcement: ''
}

export function useServerSettingsState(): readonly [ServerSettingsState, Dispatch<StatePatch<ServerSettingsState>>] {
  return useReducer(mergeState<ServerSettingsState>, initialServerSettingsState)
}
