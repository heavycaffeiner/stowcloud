import type { ThemePref } from '@/shared/theme/color-scheme'
import { persistedSignal } from '../../hooks/persisted-signal'

export const theme = persistedSignal<ThemePref>('sc.theme', ['system', 'light', 'dark'], 'system')
