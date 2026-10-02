import { persistedSignal } from '../../hooks/persisted-signal'
import type { ThemePref } from '../../ui/mdui-runtime'

export const theme = persistedSignal<ThemePref>('sc.theme', ['system', 'light', 'dark'], 'system')
