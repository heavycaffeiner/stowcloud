// web/src/lib/state/browse.svelte.ts —, §5 /
// Svelte 5 runes class. No global store library.
//
// Rows are addressed by absolute index within the server's sorted listing
// and fetched in on-demand windows (whatever FileTable is actually
// rendering), not as an ever-growing "infinite scroll" prefix. A sparse,
// LRU-bounded cache holds whatever has been loaded so far; everything else
// renders as a placeholder until its window is fetched.
import { SvelteMap, SvelteSet } from 'svelte/reactivity'
import { api, ApiError, type Entry, type Order, type SortKey } from '../api/client'
import { events } from './events'
import { reconcile, selectAll as selAll, selectOnly as selOnly, selectRange as selRange, toggle as selToggle } from './selection'

export interface Sort {
  key: SortKey
  order: Order
}

const PAGE_LIMIT = 200
/** Upper bound on how many rows are ever held in memory at once (task requirement: don't accumulate all 100k rows after a long scroll). */
const MAX_CACHED_ROWS = 2000
/** Coalesces rapid scroll events into a single request per "settle" (mirrors the 150ms idle rule applies to thumbnails). */
const WINDOW_DEBOUNCE_MS = 80

/** Toolbar preferences that outlive the page. Same `sc.`-prefixed
 *  `localStorage` keys and same swallow-everything guards as `state/ui.svelte.ts`
 *  (private mode and a full quota both throw on plain reads and writes). */
function loadPref<T extends string>(key: string, allowed: readonly T[], fallback: T): T {
  try {
    const saved = localStorage.getItem(key)
    if (allowed.includes(saved as T)) return saved as T
  } catch {
    /* ignore */
  }
  return fallback
}

function savePref(key: string, value: string): void {
  try {
    localStorage.setItem(key, value)
  } catch {
    /* ignore */
  }
}

const VIEWS = ['list', 'grid'] as const
const DENSITIES = ['compact', 'comfortable', 'spacious'] as const

export class BrowseState {
  path = $state<string>('/')
  total = $state(0)
  dirEtag = $state<string | null>(null)
  sort = $state<Sort>({ key: 'name', order: 'asc' })
  loading = $state(false)
  /** True while a windowed fetch (scroll-driven) is in flight. */
  loadingWindow = $state(false)
  error = $state<ApiError | null>(null)
  selection = new SvelteSet<string>()
  /** Roving-tabindex focus cursor: absolute row index, not a name — a
   *  not-yet-loaded row can still be "focused" while its window fetches. */
  focusedIndex = $state<number | null>(null)
  /** Grid-vs-list and row density are toolbar toggles, and a toggle that
   *  resets on every reload is one the user has to re-set on every reload.
   *  Accessors rather than plain fields so the write-through happens wherever
   *  the assignment is, which is the toolbar in `b/[...path]/+page.svelte`. */
  #view = $state<'list' | 'grid'>(loadPref('sc.view', VIEWS, 'list'))
  #density = $state<'compact' | 'comfortable' | 'spacious'>(
    loadPref('sc.density', DENSITIES, 'comfortable')
  )

  get view(): 'list' | 'grid' {
    return this.#view
  }
  set view(v: 'list' | 'grid') {
    this.#view = v
    savePref('sc.view', v)
  }

  get density(): 'compact' | 'comfortable' | 'spacious' {
    return this.#density
  }
  set density(d: 'compact' | 'comfortable' | 'spacious') {
    this.#density = d
    savePref('sc.density', d)
  }

  /** Sparse cache of loaded rows, keyed by absolute index in the sorted listing. */
  #rows = new SvelteMap<number, Entry>()
  /** name -> index, kept in lockstep with #rows so selection (by name) can resolve a position. */
  #index = new Map<string, number>()
  /** Recency queue for LRU eviction of #rows. Selected rows are never evicted
   *  (see #evictIfNeeded) — that is what keeps "selection survives an
   *  unloaded gap" true even after scrolling far away and back. */
  #lru: number[] = []

  #listing: string | null = null
  #anchor: string | null = null
  #generation = 0
  #windowAbort: AbortController | null = null
  #windowTimer: ReturnType<typeof setTimeout> | null = null
  #pendingRange: { start: number; end: number } | null = null
  /** Unsubscribes the live-invalidation watch (`state/events.ts`) for
   *  whichever directory `open()` last subscribed to — reassigned, never
   *  left dangling, so a stale directory never keeps triggering `refresh()`
   *  after the user has navigated away from it ("the
   *  subscription is limited to the directory currently being viewed"). */
  #eventsUnsub: (() => void) | null = null

  /** Entries currently selected, resolved from the cache (by name -> index -> row). */
  readonly selected = $derived.by((): Entry[] => {
    const out: Entry[] = []
    for (const name of this.selection) {
      const idx = this.#index.get(name)
      const entry = idx !== undefined ? this.#rows.get(idx) : undefined
      if (entry) out.push(entry)
    }
    return out
  })
  readonly totalSelectedSize = $derived(this.selected.reduce((a, e) => a + e.size, 0))
  readonly focusedName = $derived(
    this.focusedIndex !== null ? (this.#rows.get(this.focusedIndex)?.name ?? null) : null
  )

  /** Read a loaded row, or undefined if it hasn't been fetched (yet). Reactive. */
  rowAt(index: number): Entry | undefined {
    return this.#rows.get(index)
  }

  async open(path: string): Promise<void> {
    const gen = ++this.#generation
    this.path = path
    this.loading = true
    this.error = null
    this.selection.clear()
    this.#anchor = null
    this.#cancelPendingWindow()
    this.#rows.clear()
    this.#index.clear()
    this.#lru = []
    this.focusedIndex = null

    // Watch the directory we're actually about to be looking at, not the
    // one we just left — so a change SMB/a sync client/another browser
    // tab makes while this page is open shows up without a manual refresh.
    // Unsubscribe-then-resubscribe even when navigating
    // to the same path: `open()` is also how a sort change re-lists, and
    // there is no cheaper way to tell "still this path" apart from "this
    // path again after leaving it" that's worth the bookkeeping.
    this.#eventsUnsub?.()
    this.#eventsUnsub = events.subscribe(path, () => void this.refresh())
    try {
      const res = await api.list(path, { sort: this.sort.key, order: this.sort.order, limit: PAGE_LIMIT })
      if (gen !== this.#generation) return
      this.total = res.total
      this.dirEtag = res.dir_etag
      this.#listing = res.listing
      this.#applyPage(0, res.entries)
      this.focusedIndex = res.entries.length > 0 ? 0 : null
    } catch (err) {
      if (gen !== this.#generation) return
      this.error = err instanceof ApiError ? err : new ApiError(500, { code: 'internal', message: String(err) })
    } finally {
      if (gen === this.#generation) this.loading = false
    }
  }

  /**
   * Requests the window [start, end) be loaded, debounced so a fast scroll
   * (or a single big jump) issues one request per "settle" instead of one
   * per scroll event — / task requirement #2.
   */
  scheduleWindow(start: number, end: number): void {
    this.#pendingRange = { start, end }
    if (this.#windowTimer) clearTimeout(this.#windowTimer)
    this.#windowTimer = setTimeout(() => {
      this.#windowTimer = null
      const range = this.#pendingRange
      this.#pendingRange = null
      if (range) void this.#ensureWindow(range.start, range.end)
    }, WINDOW_DEBOUNCE_MS)
  }

  async #ensureWindow(start: number, end: number): Promise<void> {
    if (!this.#listing || end <= start) return
    let allLoaded = true
    for (let i = start; i < end; i++) {
      if (!this.#rows.has(i)) {
        allLoaded = false
        break
      }
    }
    if (allLoaded) return

    // A newer window supersedes whatever was in flight for a window the
    // user has since scrolled past (task requirement #3: AbortController).
    this.#windowAbort?.abort()
    const ac = new AbortController()
    this.#windowAbort = ac
    const gen = this.#generation
    this.loadingWindow = true
    try {
      const res = await api.list(this.path, {
        listing: this.#listing,
        offset: start,
        limit: end - start,
        signal: ac.signal
      })
      if (ac.signal.aborted || gen !== this.#generation) return
      if (res.stale) {
        // Server discarded our session (dir changed underneath us). Adopt
        // the fresh session and drop the (now positionally untrustworthy)
        // cache, except rows backing the current selection — selection is
        // kept by name, never index, so those survive regardless.
        this.total = res.total
        this.dirEtag = res.dir_etag
        this.#listing = res.listing
        this.#dropUnselectedRows()
      }
      this.#applyPage(start, res.entries)
    } catch (err) {
      if (ac.signal.aborted) return
      // A failed windowed fetch shouldn't nuke the whole page — the
      // placeholder rows simply stay placeholders and the next scroll tick
      // (or an explicit retry) tries again.
      if (gen === this.#generation && !(err instanceof ApiError)) {
        // swallow — genuinely unexpected errors still leave placeholders up
      }
    } finally {
      if (ac === this.#windowAbort) this.loadingWindow = false
    }
  }

  #cancelPendingWindow(): void {
    this.#windowAbort?.abort()
    this.#windowAbort = null
    if (this.#windowTimer) {
      clearTimeout(this.#windowTimer)
      this.#windowTimer = null
    }
    this.#pendingRange = null
  }

  async resort(s: Sort): Promise<void> {
    this.sort = s
    await this.open(this.path)
  }

  /** Re-lists the same path, preserving scroll position and selection (by name). */
  async refresh(): Promise<void> {
    const gen = ++this.#generation
    // deliberately not setting `loading = true` — this is a silent background
    // refresh (WS invalidation / post-upload), not a navigation.
    try {
      // Re-fetch at least as much as we had cached, bounded by MAX_CACHED_ROWS
      // (the cache can never exceed that anyway) so this never balloons into
      // an unbounded request no matter how far the user has scrolled.
      const limit = Math.min(Math.max(PAGE_LIMIT, this.#rows.size), MAX_CACHED_ROWS)
      const res = await api.list(this.path, { sort: this.sort.key, order: this.sort.order, limit })
      if (gen !== this.#generation) return
      this.total = res.total
      this.dirEtag = res.dir_etag
      this.#listing = res.listing

      const freshNames = new Set(res.entries.map((e) => e.name))
      // A name we can't verify this round (it was cached beyond the
      // refetched prefix) is left alone rather than dropped — we only prune
      // selection entries we can positively confirm are gone.
      const stillKnown = new Set(freshNames)
      for (const name of this.selection) {
        const idx = this.#index.get(name)
        if (idx === undefined || idx >= res.entries.length) stillKnown.add(name)
      }
      reconcile(this.selection, stillKnown)

      this.#dropUnselectedRows()
      this.#applyPage(0, res.entries)

      if (this.focusedIndex !== null && !this.#rows.has(this.focusedIndex)) {
        this.focusedIndex = res.entries.length > 0 ? 0 : null
      }
    } catch {
      // silent refreshes swallow errors — the visible listing stays as-is
    }
  }

  // ── row cache (sparse, index-addressed, LRU-bounded) ──

  #applyPage(offset: number, entries: Entry[]): void {
    entries.forEach((e, i) => this.#setRow(offset + i, e))
  }

  #setRow(index: number, entry: Entry): void {
    const occupant = this.#rows.get(index)
    if (occupant && occupant.name !== entry.name) {
      this.#index.delete(occupant.name)
    }
    const priorIndexForName = this.#index.get(entry.name)
    if (priorIndexForName !== undefined && priorIndexForName !== index) {
      this.#rows.delete(priorIndexForName)
    }
    this.#rows.set(index, entry)
    this.#index.set(entry.name, index)
    this.#touchLru(index)
    this.#evictIfNeeded()
  }

  #touchLru(index: number): void {
    const i = this.#lru.indexOf(index)
    if (i !== -1) this.#lru.splice(i, 1)
    this.#lru.push(index)
  }

  #evictIfNeeded(): void {
    while (this.#rows.size > MAX_CACHED_ROWS) {
      const victimPos = this.#lru.findIndex((idx) => {
        const e = this.#rows.get(idx)
        return e !== undefined && !this.selection.has(e.name)
      })
      if (victimPos === -1) break // everything currently cached is selected — nothing safe to evict
      const idx = this.#lru[victimPos]
      this.#lru.splice(victimPos, 1)
      const e = this.#rows.get(idx)
      this.#rows.delete(idx)
      if (e) this.#index.delete(e.name)
    }
  }

  /** Drops every cached row that is not part of the current selection. */
  #dropUnselectedRows(): void {
    for (const [idx, e] of [...this.#rows]) {
      if (!this.selection.has(e.name)) {
        this.#rows.delete(idx)
        this.#index.delete(e.name)
      }
    }
    this.#lru = this.#lru.filter((idx) => this.#rows.has(idx))
  }

  // ── selection (§9: kept by name, roving tabindex) ──

  selectOnly(entry: Entry): void {
    selOnly(this.selection, entry.name)
    this.#pinIndex(entry)
    this.#anchor = entry.name
    this.focusedIndex = this.#index.get(entry.name) ?? this.focusedIndex
  }

  toggle(entry: Entry): void {
    selToggle(this.selection, entry.name)
    if (this.selection.has(entry.name)) this.#pinIndex(entry)
    this.#anchor = entry.name
    this.focusedIndex = this.#index.get(entry.name) ?? this.focusedIndex
  }

  /**
   * Shift-click / Shift+Arrow range select. Only rows currently cached
   * between the anchor and the target can be enumerated by name — a range
   * that reaches into a still-unloaded gap selects whatever is known and
   * leaves the rest for the next pass once that window loads. If the anchor
   * itself has fallen out of the cache (selection.ts's own contract for an
   * unresolvable anchor), degrade to a plain single-row selection.
   */
  selectRangeTo(entry: Entry): void {
    this.#pinIndex(entry)
    const anchorName = this.#anchor ?? entry.name
    const anchorIndex = this.#index.get(anchorName)
    const targetIndex = this.#index.get(entry.name)
    if (anchorIndex === undefined || targetIndex === undefined) {
      this.selectOnly(entry)
      return
    }
    const [lo, hi] = anchorIndex <= targetIndex ? [anchorIndex, targetIndex] : [targetIndex, anchorIndex]
    this.selection.clear()
    for (let i = lo; i <= hi; i++) {
      const e = this.#rows.get(i)
      if (e) this.selection.add(e.name)
    }
    this.focusedIndex = targetIndex
  }

  /** Selects every currently loaded row. A true directory-wide "select all"
   *  for a 100k-row listing would need a dedicated bulk endpoint (out of
   *  scope here) — this mirrors the prior infinite-scroll behavior, which
   *  was likewise bounded to whatever had been fetched so far. */
  selectAll(): void {
    selAll(this.selection, [...this.#rows.values()].map((e) => e.name))
  }

  clearSelection(): void {
    this.selection.clear()
    this.#anchor = null
  }

  /** `delta` is `±1` for FileTable's up/down. FileGrid reuses this same
   *  cursor for its own up/down (`±columns`, to move a visual row) and
   *  left/right (`±1`) — the maths below never assumed 1, only the type did. */
  moveFocus(delta: number, extendSelection: boolean): void {
    if (this.total === 0) return
    const cur = this.focusedIndex ?? 0
    const next = Math.min(Math.max(cur + delta, 0), this.total - 1)
    this.focusedIndex = next
    const entry = this.#rows.get(next)
    if (!entry) return // window fetch (scroll effect) will resolve this shortly
    if (extendSelection) {
      this.selectRangeTo(entry)
    } else {
      this.#anchor = entry.name
    }
  }

  #pinIndex(entry: Entry): void {
    const idx = this.#index.get(entry.name)
    if (idx !== undefined) this.#touchLru(idx)
  }
}

/** App-wide singleton: one directory listing is open at a time. */
export const browse = new BrowseState()
