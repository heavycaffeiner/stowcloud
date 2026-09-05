<script lang="ts">
  // Virtual-scrolled directory table., §9.
  //
  // Accessibility note: a11y spec asks for a "roving tabindex" so the whole
  // 100k-row grid is a single tab stop. With virtualization, rows that are
  // scrolled out of view are not in the DOM, so real per-row DOM focus can't
  // be moved to them ahead of time. The correct adaptation for virtualized
  // widgets (per WAI-ARIA APG) is: the grid container itself is the one tab
  // stop (tabindex=0), a `focusedName` cursor tracks the "virtually focused"
  // row, and `aria-activedescendant` points at that row's id once it is
  // scrolled into view and rendered. Net effect for the user is identical,
  // one Tab stop, arrow keys move, with a virtualization-safe mechanism.
  import { t } from '../i18n'
  import type { Entry } from '../api/client'
  import type { Perms } from '../api/types'
  import { ui } from '../store/ui.store'
  import { selection } from '../store/selection.store'
  import { view } from '../store/view.store'
  import {
    computeScaleMapping,
    computeWindow,
    documentScrollTop,
    effectiveViewportHeight,
    rowIndexToScrollTop
  } from '../virtual/windowing'
  import FileRow from './FileRow.svelte'
  import FileRowSkeleton from './FileRowSkeleton.svelte'
  import { indicesInRect, type Rect } from './marquee'

  interface Props {
    entries: readonly Entry[]
    total: number
    dirs: number
    loading: boolean
    loadingMore: boolean
    requestMore: () => void
    perms: Perms
    onopen: (entry: Entry) => void
    oncontextmenu: (entry: Entry, e: MouseEvent) => void
    onrename?: () => void
    ondelete?: () => void
    onsearchfocus?: () => void
  }

  let { entries, total, loading, loadingMore, requestMore, onopen, oncontextmenu, onrename, ondelete, onsearchfocus }: Props =
    $props()

  const ROW_HEIGHT = $derived({ compact: 40, comfortable: 48, spacious: 56 }[view.state.density])
  const OVERSCAN = 8

  // The address bar on a phone only collapses on scroll when *the document*
  // scrolls -- this component used to be its own `overflow: auto` box (see
  // the removed `.sc-file-table { overflow: auto }` below) sitting inside a
  // shell that clipped everything else, so the document's own scrollHeight
  // never exceeded the viewport and the browser had nothing to react to.
  // The fix moves the scroll container from this element to the page: the
  // spacer/window pair below now sit in normal document flow (their height
  // really does push `document.scrollHeight` taller), and `scrollTop` /
  // `viewportHeight` -- the only two numbers `computeWindow` ever needed --
  // are read from `window.scrollY` / the visual viewport instead of from a
  // `scroll` event on this element. `computeWindow` itself didn't change at
  // all; only where these two numbers come from did (see windowing.ts's
  // `documentScrollTop`/`effectiveViewportHeight` doc comments).
  let viewportEl: HTMLDivElement | undefined = $state()
  let viewportDocumentTop = $state(0)
  let scrollTop = $state(0)
  let viewportH = $state(0)

  // Re-measured on every scroll/resize rather than cached once: content
  // above the table (search results panel, wrapped breadcrumbs, the
  // selection bar) can change height without the window itself scrolling or
  // resizing, which would leave a stale offset behind otherwise. A
  // `getBoundingClientRect()` read here is cheap next to everything else a
  // scroll handler already does.
  function measure(): void {
    if (!viewportEl) return
    viewportDocumentTop = viewportEl.getBoundingClientRect().top + window.scrollY
    scrollTop = documentScrollTop(window.scrollY, viewportDocumentTop)
    viewportH = effectiveViewportHeight(window.visualViewport?.height, window.innerHeight)
  }

  $effect(() => {
    // `visualViewport`'s own `resize` fires while a mobile browser's chrome
    // is animating open/closed, ahead of (or instead of, on some engines)
    // `window`'s `resize` -- without listening to it directly the rendered
    // window would lag a beat behind the true visible height during exactly
    // the animation this whole change exists to allow.
    window.addEventListener('scroll', measure, { passive: true })
    window.addEventListener('resize', measure)
    window.visualViewport?.addEventListener('resize', measure)
    return () => {
      window.removeEventListener('scroll', measure)
      window.removeEventListener('resize', measure)
      window.visualViewport?.removeEventListener('resize', measure)
    }
  })

  $effect(() => {
    // Re-measure on mount and whenever the loaded rows change (a fresh
    // directory's entries are a new array, not just a longer one): content
    // above this element (breadcrumbs wrapping, the search-results panel,
    // the selection bar) can change height between folders without the
    // window itself scrolling or resizing, which would otherwise leave a
    // stale document-top offset behind until the next real scroll/resize
    // event.
    entries
    measure()
  })

  // The spacer (and therefore the scrollbar) is sized
  // from the directory's *total* row count, not from how many rows happen
  // to be loaded: that is what lets the scrollbar tell the truth (and a
  // drag-to-50% jump land near row 50,000) from the very first render.
  const win = $derived(
    computeWindow({
      scrollTop,
      viewportHeight: viewportH,
      rowHeight: ROW_HEIGHT,
      itemCount: total,
      overscan: OVERSCAN
    })
  )

  interface Row {
    index: number
    entry: Entry | undefined
  }
  /** Loaded rows render normally; not-yet-loaded indices render as a FileRowSkeleton placeholder. */
  const slice = $derived.by((): Row[] => {
    const out: Row[] = []
    for (let i = win.start; i < win.end; i++) out.push({ index: i, entry: entries[i] })
    return out
  })

  function domId(name: string): string {
    return `sc-row-${encodeURIComponent(name).replace(/%/g, '_')}`
  }

  $effect(() => {
    // Listings are a forward-only cursor walk now: "load the window I am
    // looking at" means "keep asking for the next page until it covers this
    // window". Guarded on `loadingMore` so a scroll does not spam requests
    // while one is already in flight.
    if (!loadingMore && win.end > entries.length) requestMore()
  })

  /** Rows the range/all/extend actions can name: only what has actually
   *  loaded, same limit `selection.range` itself documents. */
  const loadedNames = $derived(entries.map((e) => e.name))

  const focusedName = $derived(entries[selection.state.focused ?? -1]?.name ?? null)

  function scrollRowIntoView(index: number): void {
    if (!viewportEl) return
    // Uses the same scale mapping as computeWindow so this stays correct
    // even past the ~15M px threshold where scrollTop no longer addresses
    // rows 1:1. `rowTop`/`rowBottom` are in this
    // element's own local scroll coordinates (same as `scrollTop` above);
    // the document scrolls now, so reaching them means scrolling the
    // *window* to `viewportDocumentTop + <local offset>`, not setting
    // `viewportEl.scrollTop` (which no longer does anything; the element
    // isn't a scroll container anymore).
    const mapping = computeScaleMapping(total, ROW_HEIGHT)
    const rowTop = rowIndexToScrollTop(index, mapping, ROW_HEIGHT)
    const rowBottom = rowIndexToScrollTop(index + 1, mapping, ROW_HEIGHT)
    if (rowTop < scrollTop) {
      window.scrollTo({ top: viewportDocumentTop + rowTop })
    } else if (rowBottom > scrollTop + viewportH) {
      window.scrollTo({ top: viewportDocumentTop + rowBottom - viewportH })
    }
  }

  /**
   * The rows a rubber-band drag has swept over, for the page that owns the
   * drag (`b/[...path]/+page.svelte`). The geometry is here because this is
   * where it is known; the gesture is there because it starts in the blank
   * space below this element.
   *
   * Only loaded rows can be named, so a rectangle thrown across an unfetched
   * gap picks up whatever is in memory. Same limit as `selection.range`.
   */
  export function entriesInRect(rect: Rect): Entry[] {
    const out: Entry[] = []
    for (const i of indicesInRect(rect, {
      top: viewportDocumentTop,
      left: 0,
      rowHeight: ROW_HEIGHT,
      columnPitch: 0,
      cellWidth: 0,
      columns: 1,
      startIndex: 0,
      count: total
    })) {
      const entry = entries[i]
      if (entry) out.push(entry)
    }
    return out
  }

  function onRowClick(e: MouseEvent, entry: Entry, index: number): void {
    if (e.shiftKey) {
      selection.range(loadedNames, entry.name)
      return
    }
    if (e.ctrlKey || e.metaKey) {
      selection.toggle(entry.name, index)
      return
    }
    // A plain click selects; a double click opens. Same as a desktop file
    // manager, and the same on every width.
    //
    // Shift and ctrl/cmd above are the range and the toggle, unchanged: they
    // have no other meaning here and a mouse user reaching for a range
    // expects them.
    selection.only(entry.name, index)
  }

  /** Moves the roving cursor and, only when extending, ranges to it. A plain
   *  arrow press moves the cursor without changing the selection. */
  function moveFocus(delta: number, extend: boolean): void {
    const next = Math.min(Math.max((selection.state.focused ?? 0) + delta, 0), total - 1)
    selection.focus(next)
    if (extend) {
      const name = entries[next]?.name
      if (name) selection.range(loadedNames, name)
    }
  }

  function onKeydown(e: KeyboardEvent): void {
    if (total === 0) return

    switch (e.key) {
      case 'ArrowDown':
      case 'ArrowUp': {
        e.preventDefault()
        moveFocus(e.key === 'ArrowDown' ? 1 : -1, e.shiftKey)
        scrollRowIntoView(selection.state.focused ?? 0)
        break
      }
      case ' ': {
        e.preventDefault()
        if (selection.state.focused !== null) {
          const entry = entries[selection.state.focused]
          if (entry) selection.toggle(entry.name, selection.state.focused)
        }
        break
      }
      case 'a':
      case 'A':
        if (e.ctrlKey || e.metaKey) {
          e.preventDefault()
          selection.all(loadedNames)
        }
        break
      case 'Enter': {
        const entry = selection.state.focused !== null ? entries[selection.state.focused] : undefined
        if (entry) onopen(entry)
        break
      }
      case 'F2':
        e.preventDefault()
        onrename?.()
        break
      case 'Escape':
        // Keyboard counterpart of clicking the blank area below the listing.
        if (selection.state.names.size > 0) {
          e.preventDefault()
          selection.clear()
        }
        break
      case 'Delete':
        e.preventDefault()
        ondelete?.()
        break
      case '/':
        e.preventDefault()
        onsearchfocus?.()
        break
    }
  }

  const activeDescendant = $derived(
    focusedName && slice.some((r) => r.entry?.name === focusedName) ? domId(focusedName) : undefined
  )
</script>

<div
  bind:this={viewportEl}
  class="sc-file-table"
  class:sc-file-table--reserve-bar={ui.state.compact}
  class:sc-file-table--reserve-selection={selection.state.names.size > 0}
  data-density={view.state.density}
  role="grid"
  aria-multiselectable="true"
  aria-rowcount={total}
  aria-label={t('table.file_list')}
  aria-activedescendant={activeDescendant}
  aria-busy={loadingMore}
  tabindex="0"
  onkeydown={onKeydown}
>
  {#if total === 0 && !loading}
    <p class="sc-file-table__empty">{t('common.folder_empty')}</p>
  {:else}
    <div class="sc-file-table__spacer" style:height="{win.totalHeight}px">
      <div class="sc-file-table__window" style:transform="translate3d(0,{win.padTop}px,0)">
        {#each slice as row (row.entry ? row.entry.name : `sc-ph-${row.index}`)}
          {#if row.entry}
            <FileRow
              entry={row.entry}
              rowIndex={row.index + 1}
              selected={selection.state.names.has(row.entry.name)}
              focused={focusedName === row.entry.name}
              domId={domId(row.entry.name)}
              onclick={(e) => onRowClick(e, row.entry as Entry, row.index)}
              ondblclick={() => onopen(row.entry as Entry)}
              oncontextmenu={(e) => {
                e.preventDefault()
                // The list container has its own handler for blank space;
                // without this the row menu and the folder menu would both
                // answer the same right-click.
                e.stopPropagation()
                oncontextmenu(row.entry as Entry, e)
              }}
              ontogglecheck={() => selection.toggle((row.entry as Entry).name, row.index)}
            />
          {:else}
            <FileRowSkeleton rowIndex={row.index + 1} />
          {/if}
        {/each}
      </div>
    </div>
  {/if}
</div>

<style>
  .sc-file-table {
    position: relative;
    flex: 1;
    /* Flex items default to min-width: auto (shrink-to-fit their content's
       min-content size). FileRow's fixed-width cells (select/size/mtime)
       don't shrink, so without this the whole shell is forced wider than
       the viewport at phone widths instead of scrolling in place. */
    min-width: 0;
    /* Was `align-items: stretch` (the flex default) via the ancestor row
       (`.sc-browse__view`) it sits in, which used to be exactly what we
       wanted: fill the clipped, viewport-bounded box the old shell handed
       down. Now that box is content-height, not viewport-height (§ shell
       comment in +layout.svelte), and stretching to fill it would just mean
       stretching to fill *itself* -- a job stretch can't do and doesn't
       need to; the element already sizes to its own content (the spacer
       below) once it stops being told to match someone else's size. */
    align-self: flex-start;
    /* No more `overflow: auto` -- this element was its own scroll
       container, which is exactly why the document never scrolled and a
       phone's browser chrome never collapsed (see the doc comment above
       `viewportEl`/`measure()`). The spacer below now sits in normal
       document flow instead, so *its* height is what grows
       `document.scrollHeight`.
       `contain: strict` (= size + layout + style + paint) required a
       definite size, which is exactly what this element no longer has --
       it used to come from the shell's flex-bounded, clipping ancestor,
       and that ancestor is gone (this is the "move as one change" the
       handoff note called out: dropping the scroller while keeping the
       old `contain: strict` would have been an invalid halfway state,
       since `strict` needs a size nothing here provides anymore).
       `contain: content` (= layout + style + paint, no `size`) keeps the
       same layout/paint isolation without claiming a definite size this
       element doesn't have. */
    contain: content;
    background: var(--m3c-surface);
    /* MD3 window classes, queried against this
       element's own inline size rather than the viewport. `container-type:
       inline-size` only contains the *inline* axis (width) -- unlike
       `contain: strict`/`size`, it has no opinion on block-size (height),
       so it stays correct now that this element's height is auto/content-
       driven instead of definite. */
    container-type: inline-size;
    container-name: sc-file-table;
  }
  .sc-file-table:focus-visible {
    outline: 3px solid var(--m3c-secondary);
    outline-offset: -3px;
  }
  /* Both bottom reservations land here, each contributed by its own class
     below and defaulting to nothing. */
  .sc-file-table {
    padding-bottom: calc(var(--sc-reserve-nav, 0px) + var(--sc-reserve-selection, 0px));
  }
  /* `focusedName` is the roving cursor `aria-activedescendant` points at, and
     it is seeded on the first row -- so FileRow's `--focused` outline drew a
     3px box around row 1 of every folder before anyone had touched anything.
     An activedescendant only means something while the grid owns focus, so
     the indicator only shows then. `:focus`, not `:focus-visible`: clicking a
     row focuses this container without the heuristic, and the cursor the
     arrow keys will move from has to be visible from that click on. */
  .sc-file-table:not(:focus) :global(.sc-row--focused) {
    outline: none;
  }
  .sc-file-table--reserve-bar {
    /* `NavigationBar` is `position: fixed` (+layout.svelte's own comment
       explains why `sticky` can't work here), so nothing reserves its
       ~64px in flow anymore. `.sc-app-shell__main`'s own `padding-bottom`
       (+layout.svelte) handles that for ordinary bounded-height panes
       (`/settings`, `/admin`), but this element's real height escapes
       *past* that ancestor's box entirely (that's the whole point of
       `align-self: flex-start` + no `overflow: auto` above -- the spacer
       is real document flow, not clipped inside a viewport-bounded parent),
       so padding added anywhere upstream of here never reaches the actual
       tail end of a long list. The reservation has to live on the one
       element whose own box height genuinely equals "however tall the real
       content is" -- this one. Same formula as everywhere else the bar's
       height is reserved, compact (phone) width only -- the standard/rail
       layout has no bottom bar to clear. */
    --sc-reserve-nav: calc(var(--sc-nav-bar-height) + env(safe-area-inset-bottom, 0px));
  }
  /* The selection bar is fixed over the bottom too, at every width, for as
     long as something is selected. Reserving with padding rather than letting
     it push the list is what keeps a first click from moving the row the
     second click of a double click is aimed at (see `b/[...path]/+page.svelte`). */
  .sc-file-table--reserve-selection {
    --sc-reserve-selection: 80px;
  }
  .sc-file-table__window {
    will-change: transform;
  }
  .sc-file-table__empty {
    /* Was `position: absolute; inset: 0` sized off this element's own box --
       that box was always exactly the viewport-bounded area before. Now its
       height is auto/content-driven (see `.sc-file-table` above), so an
       empty directory would size this element to 0 height and collapse the
       message with it. A plain in-flow block with its own padding doesn't
       depend on the parent having a height at all. */
    display: flex;
    align-items: center;
    justify-content: center;
    padding-block: 64px;
    color: var(--m3c-on-surface-variant);
  }
</style>
