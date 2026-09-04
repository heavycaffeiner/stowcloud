import { createStore, type StoreApi } from 'zustand/vanilla'

export interface UploadItem {
  readonly id: string
  readonly name: string
  readonly dest: string
  readonly total: number
  readonly sent: number
  readonly rate: number
  readonly etaSec: number
  readonly status: 'queued' | 'uploading' | 'paused' | 'done' | 'error' | 'canceled'
  readonly message?: string
  readonly messageParams?: Record<string, string | number>
}

export interface UploadSnapshot {
  readonly items: readonly UploadItem[]
  readonly open: boolean
}

export type UploadAction =
  | { type: 'QUEUE_ITEM'; item: UploadItem }
  | { type: 'PATCH_ITEM'; id: string; patch: Partial<UploadItem> }
  | { type: 'DISMISS_ITEM'; id: string }
  | { type: 'CLEAR_FINISHED' }
  | { type: 'SET_OPEN'; open: boolean }

export function patchItemInList(
  items: readonly UploadItem[],
  id: string,
  patch: Partial<UploadItem>
): readonly UploadItem[] {
  const index = items.findIndex((x) => x.id === id)
  if (index === -1) return items
  const next = [...items]
  next[index] = { ...items[index], ...patch }
  return next
}

export function uploadReducer(state: UploadSnapshot, action: UploadAction): UploadSnapshot {
  switch (action.type) {
    case 'QUEUE_ITEM':
      return {
        open: true,
        items: [...state.items, action.item]
      }
    case 'PATCH_ITEM':
      return {
        ...state,
        items: patchItemInList(state.items, action.id, action.patch)
      }
    case 'DISMISS_ITEM':
      return {
        ...state,
        items: state.items.filter((x) => x.id !== action.id)
      }
    case 'CLEAR_FINISHED':
      return {
        ...state,
        items: state.items.filter((x) => x.status !== 'done' && x.status !== 'canceled')
      }
    case 'SET_OPEN':
      return {
        ...state,
        open: action.open
      }
    default:
      return state
  }
}

export interface UploadStore extends StoreApi<UploadSnapshot> {
  dispatch(action: UploadAction): void
}

export function createUploadStore(): UploadStore {
  const store = createStore<UploadSnapshot>(() => ({
    items: [],
    open: false
  }))

  return {
    ...store,
    dispatch(action: UploadAction): void {
      store.setState((prev) => uploadReducer(prev, action))
    }
  }
}
