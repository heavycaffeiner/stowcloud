import { persistedSignal } from '../../lib/persisted-signal'

export const sidebar = persistedSignal<'expanded' | 'collapsed'>('sc.sidebar', ['expanded', 'collapsed'], 'expanded')

export function toggleSidebar(): void {
  sidebar.value = sidebar.value === 'expanded' ? 'collapsed' : 'expanded'
}
