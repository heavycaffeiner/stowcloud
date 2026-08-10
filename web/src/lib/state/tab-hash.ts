// web/src/lib/state/tab-hash.ts — pure decision for the tab <-> URL-hash sync
// used by the tabbed screens (`/settings`, `/admin`).
//
// The sync has to run both ways. Writing only tab -> URL looks right until
// someone opens `/settings#appearance` while already on `/settings`: SvelteKit
// treats that as a same-document hash change, the tab never moves, and the
// effect overwrites the URL back to the tab already on screen, so the link
// silently does nothing.
//
// What makes the other direction tricky is that `replaceState` writes history
// without touching `page.url`. The hash a component reads therefore does not
// move when the tab does; it moves on a real navigation and nowhere else.
// `seen` is the last hash read off `page.url`, which is what tells a genuine
// external change apart from that stale read.

export interface TabHashSync {
  /** Hash to remember as last observed; feed it back on the next call. */
  seen: string
  /** Tab that should be selected after this step. */
  tab: string
  /** Hash to write to history, or null when the URL already agrees. */
  write: string | null
}

export function syncTabHash(hash: string, seen: string, tab: string, valid: readonly string[]): TabHashSync {
  if (hash !== seen && valid.includes(hash)) return { seen: hash, tab: hash, write: null }
  return { seen: hash, tab, write: hash === tab ? null : tab }
}
