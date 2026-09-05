// The upload queue's state. The Worker that fills it lives in
// `upload/queue.ts`; this file only holds what the tray renders.
import { defineStore } from './create.svelte'

export type UploadStatus = 'queued' | 'uploading' | 'paused' | 'done' | 'error' | 'canceled'

export interface UploadItem {
  readonly id: string
  readonly name: string
  readonly dest: string
  readonly total: number
  readonly sent: number
  readonly rate: number
  readonly etaSec: number
  readonly status: UploadStatus
  /** A catalogue key, never text: the tray resolves it in the reader's
   *  language at render time. */
  readonly message?: string
  readonly messageParams?: Record<string, string | number>
}

export interface UploadState {
  readonly items: readonly UploadItem[]
  readonly open: boolean
}

export const uploads = defineStore({ items: [], open: false } as UploadState, (set, get) => ({
  queue(item: UploadItem): void {
    set({ items: [...get().items, item], open: true })
  },

  patch(id: string, patch: Partial<UploadItem>): void {
    set({ items: get().items.map((item) => (item.id === id ? { ...item, ...patch } : item)) })
  },

  dismiss(id: string): void {
    set({ items: get().items.filter((item) => item.id !== id) })
  },

  clearFinished(): void {
    set({ items: get().items.filter((item) => item.status !== 'done' && item.status !== 'canceled') })
  },

  setOpen(open: boolean): void {
    set({ open })
  },

  statusOf(id: string): UploadStatus | undefined {
    return get().items.find((item) => item.id === id)?.status
  }
}))
