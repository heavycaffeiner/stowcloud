// How the file browser is laid out and ordered. Each choice outlives the page:
// a toggle that resets on reload is one the user has to re-set on reload.
import { batch } from '@preact/signals-react'
import { persistedSignal } from '../../lib/persisted-signal'
import type { Order, SortKey } from './api'

export type ViewMode = 'list' | 'grid'
export type Density = 'compact' | 'comfortable' | 'spacious'

const DENSITIES: readonly Density[] = ['compact', 'comfortable', 'spacious']

export const viewMode = persistedSignal<ViewMode>('sc.view', ['list', 'grid'], 'list')
export const density = persistedSignal<Density>('sc.density', DENSITIES, 'comfortable')
export const sortKey = persistedSignal<SortKey>('sc.sort', ['name', 'size', 'mtime', 'kind'], 'name')
export const sortOrder = persistedSignal<Order>('sc.order', ['asc', 'desc'], 'asc')
/** Off until asked for, at any width. */
export const detailsPanel = persistedSignal<'open' | 'closed'>('sc.details', ['open', 'closed'], 'closed')

export function toggleViewMode(): void {
  viewMode.value = viewMode.value === 'list' ? 'grid' : 'list'
}

export function cycleDensity(): void {
  density.value = DENSITIES[(DENSITIES.indexOf(density.value) + 1) % DENSITIES.length]
}

/** Sorting by the current key again flips the order; a new key starts ascending. */
export function chooseSort(key: SortKey): void {
  batch(() => {
    sortOrder.value = sortKey.value === key && sortOrder.value === 'asc' ? 'desc' : 'asc'
    sortKey.value = key
  })
}

export function toggleDetails(): void {
  detailsPanel.value = detailsPanel.value === 'open' ? 'closed' : 'open'
}
