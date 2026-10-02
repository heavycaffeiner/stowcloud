// How the file browser is laid out. Each choice outlives the page:
// a toggle that resets on reload is one the user has to re-set on reload.
import { persistedSignal } from '../../../hooks/persisted-signal'

export type ViewMode = 'list' | 'grid'
export type Density = 'compact' | 'comfortable' | 'spacious'

const DENSITIES: readonly Density[] = ['compact', 'comfortable', 'spacious']

export const viewMode = persistedSignal<ViewMode>('sc.view', ['list', 'grid'], 'list')
export const density = persistedSignal<Density>('sc.density', DENSITIES, 'comfortable')
/** Off until asked for, at any width. */
export const detailsPanel = persistedSignal<'open' | 'closed'>('sc.details', ['open', 'closed'], 'closed')

export function toggleViewMode(): void {
  viewMode.value = viewMode.value === 'list' ? 'grid' : 'list'
}

export function cycleDensity(): void {
  density.value = DENSITIES[(DENSITIES.indexOf(density.value) + 1) % DENSITIES.length]
}

export function toggleDetails(): void {
  detailsPanel.value = detailsPanel.value === 'open' ? 'closed' : 'open'
}
