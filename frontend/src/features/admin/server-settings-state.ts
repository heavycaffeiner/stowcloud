import { usePatchState } from '../../lib/store/use-component-state'
import type { ApplyOutcome } from '../../lib/api/types'

export type ServerSettingsGroup = 'smb' | 'search' | 'thumbnail' | 'archive' | 'network' | 'db' | 'homes' | 'watch' | 'rate' | 'oidc'
export type ServerSettingsValues = Record<string, unknown>
export type ServerPathPicker = { mode: 'folder' | 'file'; key: string } | null

export interface ServerSettingsState {
  values: ServerSettingsValues
  baselines: Record<ServerSettingsGroup, string | null>
  hydrated: Record<ServerSettingsGroup, boolean>
  activeGroup: ServerSettingsGroup | null
  validationError: string | null
  outcome: ApplyOutcome | null
  restartOutcome: ApplyOutcome | null
  pathPicker: ServerPathPicker
  secret: string
  announcement: string
}

export type ServerSettingsPatch = Partial<ServerSettingsState> | ((state: ServerSettingsState) => Partial<ServerSettingsState>)

export const SERVER_SETTINGS_GROUPS: readonly ServerSettingsGroup[] = ['smb', 'search', 'thumbnail', 'archive', 'network', 'db', 'homes', 'watch', 'rate', 'oidc']

const initialServerSettingsState: ServerSettingsState = {
  values: {},
  baselines: Object.fromEntries(SERVER_SETTINGS_GROUPS.map((group) => [group, null])) as Record<ServerSettingsGroup, string | null>,
  hydrated: Object.fromEntries(SERVER_SETTINGS_GROUPS.map((group) => [group, false])) as Record<ServerSettingsGroup, boolean>,
  activeGroup: null,
  validationError: null,
  outcome: null,
  restartOutcome: null,
  pathPicker: null,
  secret: '',
  announcement: ''
}

export function useServerSettingsState(): readonly [ServerSettingsState, (patch: ServerSettingsPatch) => void] {
  return usePatchState(initialServerSettingsState)
}
