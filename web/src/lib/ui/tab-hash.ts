// Pure decision for the tab and URL-hash sync used by the tabbed screens
// (`/settings`, `/admin`).
//
// The sync has to run both ways. Writing only tab -> URL looks right until
// someone opens `/settings#appearance` while already on `/settings`: SvelteKit
// treats that as a same-document hash change, the tab never moves, and the
// effect overwrites the URL back to the tab already on screen, so the link
// silently does nothing.
//
// What makes the other direction tricky is that `replaceState` writes history
// without touching `page.url`. That single stale hash cannot answer both
// questions the sync asks, so each gets its own source:
//
//  - "did something navigate?" compares `page.url`'s hash against `seen`, the
//    last one read off it. It moves on a real navigation and nowhere else.
//  - "does the address bar already say this?" reads `current`, the live
//    `location.hash`. Asking `page.url` instead makes every return to the tab
//    the page loaded on look like a no-op: the URL keeps whichever tab was open
//    before it, and a reload then lands on the wrong one.

export interface TabHashSync {
  /** Hash to remember as last observed; feed it back on the next call. */
  seen: string
  /** Tab that should be selected after this step. */
  tab: string
  /** Hash to write to history, or null when the URL already agrees. */
  write: string | null
}

export function syncTabHash(
  hash: string,
  seen: string,
  current: string,
  tab: string,
  valid: readonly string[]
): TabHashSync {
  if (hash !== seen && valid.includes(hash)) return { seen: hash, tab: hash, write: null }
  return { seen: hash, tab, write: current === tab ? null : tab }
}
