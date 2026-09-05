// Which background jobs the tray is showing.
//
// Only the ids live here: each job's progress is a query, so a job started on
// one page keeps reporting after the user navigates away, and one started in
// another tab is picked up from the job list.
import { defineStore } from './create.svelte'

export interface JobTrayState {
  readonly ids: readonly string[]
  readonly open: boolean
}

export const jobTray = defineStore({ ids: [], open: false } as JobTrayState, (set, get) => ({
  /** Shows a job, opening the tray for it. Ids arrive from both a write this
   *  tab made and the server's own list, so this is idempotent. */
  track(...ids: readonly string[]): void {
    const fresh = ids.filter((id) => !get().ids.includes(id))
    if (fresh.length === 0) return
    set({ ids: [...get().ids, ...fresh], open: true })
  },

  forget(...ids: readonly string[]): void {
    set({ ids: get().ids.filter((id) => !ids.includes(id)) })
  },

  setOpen(open: boolean): void {
    set({ open })
  }
}))
