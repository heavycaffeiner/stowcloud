// Where the search surface is, and what it was opened from.
//
// One experience with two shapes: a top sheet over the page you were on when
// there is room beside it, and a page of its own when there is not. The
// button that opens it and the sheet that draws it sit on opposite sides of
// the layout, which is why the open state is here rather than in either.
import { goto } from '$app/navigation'
import { defineStore } from './create.svelte'
import { ui } from './ui.store'

export interface SearchState {
  /** The desktop top sheet. The phone route is not a state, it is a URL. */
  readonly open: boolean
  /** The folder the search was started from. It ranks that subtree up; it
   *  never confines the search, which always covers everything reachable. */
  readonly scope: string
}

export const search = defineStore({ open: false, scope: '' } as SearchState, (set) => ({
  openSheet(scope = ''): void {
    set({ open: true, scope })
  },
  close(): void {
    set({ open: false })
  }
}))

/**
 * Opens search in whichever shape the viewport calls for.
 *
 * One function so both callers agree: a sheet over a phone screen is a page
 * with extra steps, and a full navigation on a desktop throws away the folder
 * the person was looking at.
 */
export function openSearch(scope = ''): void {
  if (ui.peek().compact) {
    void goto(scope ? `/search?path=${encodeURIComponent(scope)}` : '/search')
    return
  }
  search.openSheet(scope)
}
