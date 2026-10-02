// The order the file browser asks the server for. The server sorts, so the whole folder is in this
// order even when only part of it has loaded. The choice outlives the page.
import { batch } from '@preact/signals-react'
import { persistedSignal } from '../../../hooks/persisted-signal'
import type { Order, SortKey } from '../api'

export type { Order, SortKey }

export const SORT_KEYS: readonly SortKey[] = ['name', 'size', 'mtime', 'kind']

export const SORT_LABEL_KEYS: Readonly<Record<SortKey, string>> = {
  name: /* i18n */ 'browse.sort_by_name',
  size: /* i18n */ 'browse.sort_by_size',
  mtime: /* i18n */ 'browse.sort_by_modified',
  kind: /* i18n */ 'browse.sort_by_kind'
}

export const sortKey = persistedSignal<SortKey>('sc.sort', SORT_KEYS, 'name')
export const sortOrder = persistedSignal<Order>('sc.order', ['asc', 'desc'], 'asc')

/** Sorting by the current key again flips the order; a new key starts ascending. */
export function chooseSort(key: SortKey): void {
  batch(() => {
    sortOrder.value = sortKey.value === key && sortOrder.value === 'asc' ? 'desc' : 'asc'
    sortKey.value = key
  })
}
