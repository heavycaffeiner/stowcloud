<script lang="ts">
  // FileGrid.svelte: virtual-scrolled card grid, folders above files.
  //
  // ## Why two windows instead of one
  //
  // A folder card is one line tall; a file card is a header line plus a
  // thumbnail. `computeWindow` maps a scroll offset to a row index by
  // dividing, which is only true while every row is the same height, so one
  // window over a mixed run of both card types is arithmetic that does not
  // hold. Averaging the two heights would still divide, and it would still be
  // wrong: the error grows with the number of rows above, so it shows up as
  // rows drifting out from under the pointer deep in a large directory, which
  // is the hardest kind of scroll bug to trace back to its cause.
  //
  // The alternative was teaching `computeWindow` about two row heights. That
  // means a second set of maths, a second set of edge cases at the seam, and a
  // second thing that can disagree with FileTable, which shares the module.
  // Two windows over two uniform sections keep the existing invariant exactly
  // as it is: each section divides by its own single row height, and each one
  // is right.
  //
  // The sections sit in one document scroll (the whole page scrolls, see
  // `windowing.ts`), so each measures its own top and derives its own
  // scroll offset from it. A section entirely off-screen renders nothing and
  // asks for nothing.
  //
  // The split point comes from the server (the `dirs` prop). It cannot be
  // found client-side: rows load in windows, so the entries either side of
  // the seam are usually not in memory.
  //
  // Same accessibility contract as FileTable: one tab stop (`tabindex=0` on
  // the grid container), `aria-activedescendant` tracks the "virtually
  // focused" cell, and per-cell tabindex is never given (a 100k-entry
  // directory cannot have 100k tab stops). The kebab button on each card is
  // `tabindex="-1"` for the same reason -- keyboard users reach the same menu
  // with the Menu key or Shift+F10 on the focused card.
  import { t } from '../i18n'
  import type { Entry } from '../api/client'
  import type { Perms } from '../api/types'
  import { ui } from '../store/ui.store'
  import { selection } from '../store/selection.store'
  import { view } from '../store/view.store'
  import { formatBytes } from '../format/bytes'
  import {
    computeScaleMapping,
    computeWindow,
    documentScrollTop,
    effectiveViewportHeight,
    rowIndexToScrollTop
  } from '../virtual/windowing'
  import { cellPos, sectionRows, verticalTarget } from '../virtual/grid-sections'
  import { Checkbox, Icon } from 'm3-svelte'
  import { icons, type IconName } from '../icons'
  import Thumbnail from './Thumbnail.svelte'
  import { isVideoFile } from './media-utils'
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

  let { entries, total, dirs, loading, loadingMore, requestMore, onopen, oncontextmenu, onrename, ondelete, onsearchfocus }: Props =
    $props()

  // 4px-grid card metrics, keyed by the same density control that drives
  // FileTable's row height. Both card types share a width so the two sections
  // line up on the same columns, which is what makes "the card below this one"
  // mean anything across the seam.
  const CARD = $derived(
    {
      compact: { w: 192, folderH: 44, fileH: 176, gap: 8 },
      comfortable: { w: 224, folderH: 52, fileH: 208, gap: 12 },
      spacious: { w: 256, folderH: 60, fileH: 244, gap: 16 }
    }[view.state.density]
  )
  const OVERSCAN = 3 // rows, not cells

  // Document-scroll migration: see the matching comment block in
  // FileTable.svelte. `viewportW` stays a real `bind:clientWidth` -- this
  // element still has a genuine bounded *width*; only its height became
  // content-driven, which is why the height-derived numbers come from
  // window/visualViewport reads instead.
  let viewportEl: HTMLDivElement | undefined = $state()
  let foldersEl: HTMLDivElement | undefined = $state()
  let filesEl: HTMLDivElement | undefined = $state()
  let viewportW = $state(0)
  let scrollY = $state(0)
  let viewportH = $state(0)
  let foldersTop = $state(0)
  let filesTop = $state(0)

  /** Entry the user last scrolled to the top of the viewport, kept so a
   *  column-count change can put it back. Deliberately not `$state`: it is a
   *  record of where we were, and making it reactive would feed the same
   *  scroll it exists to restore. */
  let anchorIndex = 0
  let lastColumns = 0

  function measure(): void {
    if (!viewportEl) return
    scrollY = window.scrollY
    viewportH = effectiveViewportHeight(window.visualViewport?.height, window.innerHeight)
    foldersTop = foldersEl ? foldersEl.getBoundingClientRect().top + scrollY : 0
    filesTop = filesEl ? filesEl.getBoundingClientRect().top + scrollY : 0
  }

  /**
   * Scrolling is the only thing that moves the anchor, which is why this is
   * separate from `measure`. Changing the column count also re-measures, and
   * it does so through an effect that runs *before* the one that restores the
   * anchor; recording the anchor in `measure` therefore overwrote it with a
   * reading taken against the new layout, and the restore put back a position
   * derived from the change it was supposed to undo.
   */
  function onScroll(): void {
    measure()
    anchorIndex = topVisibleIndex()
  }

  function topVisibleIndex(): number {
    if (total === 0) return 0
    const last = total - 1
    if (fileCount > 0 && filesTop > 0 && scrollY >= filesTop) {
      const row = Math.floor((scrollY - filesTop) / fileRowH)
      return Math.min(folderCount + row * columns, last)
    }
    const row = Math.floor(Math.max(0, scrollY - foldersTop) / folderRowH)
    return Math.min(row * columns, last)
  }

  $effect(() => {
    window.addEventListener('scroll', onScroll, { passive: true })
    window.addEventListener('resize', measure)
    window.visualViewport?.addEventListener('resize', measure)
    return () => {
      window.removeEventListener('scroll', onScroll)
      window.removeEventListener('resize', measure)
      window.visualViewport?.removeEventListener('resize', measure)
    }
  })

  const availableW = $derived(Math.max(CARD.w, viewportW - 32))
  const columns = $derived(Math.max(1, Math.floor((availableW + CARD.gap) / (CARD.w + CARD.gap))))
  const cardW = $derived(Math.max(120, Math.floor((availableW - (columns - 1) * CARD.gap) / columns)))
  const folderCount = $derived(Math.min(dirs, total))
  const fileCount = $derived(Math.max(0, total - folderCount))
  const folderRowH = $derived(CARD.folderH + CARD.gap)
  const fileRowH = $derived(CARD.fileH + CARD.gap)
  const folderRowCount = $derived(sectionRows(folderCount, columns))
  const fileRowCount = $derived(sectionRows(fileCount, columns))

  $effect(() => {
    // Anything that moves one section relative to the other invalidates the
    // measured tops, and none of it arrives as a scroll or resize event:
    // a fresh directory's entries are a new array (not just a longer one),
    // changing density or column count changes the folder section's height
    // and so where the file section starts. Reading `filesTop` here would
    // loop; these are the inputs.
    entries
    folderRowCount
    fileRowCount
    folderRowH
    fileRowH
    measure()
  })

  $effect(() => {
    // Opening or closing the details panel narrows this element without the
    // window resizing, so every row is repacked and whatever the user was
    // looking at slides off. Re-anchoring on the entry that was at the top
    // keeps the answer to "where am I" the same across the change. The first
    // pass only records the column count: there is nothing to restore yet.
    const cols = columns
    if (lastColumns === 0 || lastColumns === cols) {
      lastColumns = cols
      return
    }
    lastColumns = cols
    const target = anchorIndex
    const wasScrolledIn = scrollY > foldersTop
    measure()
    if (wasScrolledIn) scrollIndexToTop(target)
  })

  const folderWin = $derived(
    computeWindow({
      scrollTop: documentScrollTop(scrollY, foldersTop),
      viewportHeight: viewportH,
      rowHeight: folderRowH,
      itemCount: folderRowCount,
      overscan: OVERSCAN
    })
  )
  const fileWin = $derived(
    computeWindow({
      scrollTop: documentScrollTop(scrollY, filesTop),
      viewportHeight: viewportH,
      rowHeight: fileRowH,
      itemCount: fileRowCount,
      overscan: OVERSCAN
    })
  )

  /** A section wholly above or below the viewport renders no cards and asks
   *  for no rows. Without this, scrolling deep into the files still leaves the
   *  folder window pinned to its own last row, fetching entries nobody can
   *  see. */
  function intersects(top: number, height: number): boolean {
    return height > 0 && top < scrollY + viewportH && top + height > scrollY
  }
  const foldersActive = $derived(intersects(foldersTop, folderWin.totalHeight))
  const filesActive = $derived(intersects(filesTop, fileWin.totalHeight))

  interface Cell {
    index: number
    entry: Entry | undefined
  }
  interface Row {
    key: string
    ariaRow: number
    cells: Cell[]
  }

  function buildRows(
    win: { start: number; end: number },
    offset: number,
    count: number,
    ariaBase: number,
    prefix: string
  ): Row[] {
    const out: Row[] = []
    for (let r = win.start; r < win.end; r++) {
      const cells: Cell[] = []
      const base = r * columns
      for (let c = 0; c < columns; c++) {
        if (base + c >= count) break
        const index = offset + base + c
        cells.push({ index, entry: entries[index] })
      }
      out.push({ key: `${prefix}${r}`, ariaRow: ariaBase + r + 1, cells })
    }
    return out
  }

  const folderRows = $derived(
    foldersActive ? buildRows(folderWin, 0, folderCount, 0, 'sc-grid-d-') : []
  )
  const fileRows = $derived(
    filesActive ? buildRows(fileWin, folderCount, fileCount, folderRowCount, 'sc-grid-f-') : []
  )

  function domId(name: string): string {
    return `sc-grid-cell-${encodeURIComponent(name).replace(/%/g, '_')}`
  }

  $effect(() => {
    // One range per section rather than one range spanning both: once the
    // folder section is behind us the two are nowhere near each other, and
    // their union would be a request for every row in between. Listings are
    // a forward-only cursor walk now, so "load the window I am looking at"
    // becomes "keep asking for the next page until it covers the furthest
    // range end", guarded on `loadingMore` so a scroll does not spam it.
    const ends: number[] = []
    if (foldersActive) ends.push(Math.min(folderCount, folderWin.end * columns))
    if (filesActive) ends.push(Math.min(total, folderCount + fileWin.end * columns))
    const furthestEnd = ends.length > 0 ? Math.max(...ends) : 0
    if (!loadingMore && furthestEnd > entries.length) requestMore()
  })

  /** Document Y of the top of the row holding `index`. */
  function rowTopOf(index: number): number {
    const pos = cellPos(index, folderCount, columns)
    const top = pos.section === 0 ? foldersTop : filesTop
    const rowH = pos.section === 0 ? folderRowH : fileRowH
    const rowCount = pos.section === 0 ? folderRowCount : fileRowCount
    return top + rowIndexToScrollTop(pos.row, computeScaleMapping(rowCount, rowH), rowH)
  }

  function scrollIndexToTop(index: number): void {
    window.scrollTo({ top: rowTopOf(index) })
  }

  function scrollIndexIntoView(index: number): void {
    if (!viewportEl) return
    const pos = cellPos(index, folderCount, columns)
    const top = pos.section === 0 ? foldersTop : filesTop
    const rowH = pos.section === 0 ? folderRowH : fileRowH
    const rowCount = pos.section === 0 ? folderRowCount : fileRowCount
    // See FileTable's `scrollRowIntoView`: this scrolls the window (document
    // coordinates), because neither section is a scroll container.
    const mapping = computeScaleMapping(rowCount, rowH)
    const rowTop = top + rowIndexToScrollTop(pos.row, mapping, rowH)
    const rowBottom = top + rowIndexToScrollTop(pos.row + 1, mapping, rowH)
    if (rowTop < scrollY) {
      window.scrollTo({ top: rowTop })
    } else if (rowBottom > scrollY + viewportH) {
      window.scrollTo({ top: rowBottom - viewportH })
    }
  }

  /**
   * The cards a rubber-band drag has swept over, for the page that owns the
   * drag (`b/[...path]/+page.svelte`).
   *
   * Two sections, so two rectangles' worth of arithmetic: the folder cards and
   * the file cards have different heights and start at different places, which
   * is the same reason they get a window each.
   *
   * Only loaded rows can be named, so a rectangle thrown across an unfetched
   * gap picks up whatever is in memory. Same limit as `selection.range`.
   */
  export function entriesInRect(rect: Rect): Entry[] {
    // 16px of `padding-inline` on `.sc-file-grid__window`, and the cards are
    // laid out from there on a `CARD.w + CARD.gap` pitch.
    const left = (viewportEl?.getBoundingClientRect().left ?? 0) + window.scrollX + 16
    const common = { left, columnPitch: cardW + CARD.gap, cellWidth: cardW, columns }
    const hits = [
      ...indicesInRect(rect, {
        ...common,
        top: foldersTop,
        rowHeight: folderRowH,
        startIndex: 0,
        count: folderCount
      }),
      ...indicesInRect(rect, {
        ...common,
        top: filesTop,
        rowHeight: fileRowH,
        startIndex: folderCount,
        count: fileCount
      })
    ]
    const out: Entry[] = []
    for (const i of hits) {
      const entry = entries[i]
      if (entry) out.push(entry)
    }
    return out
  }

  /** Rows the range/all/extend actions can name: only what has actually
   *  loaded, same limit `selection.range` itself documents. */
  const loadedNames = $derived(entries.map((e) => e.name))

  const focusedName = $derived(entries[selection.state.focused ?? -1]?.name ?? null)

  function onCardClick(e: MouseEvent, entry: Entry, index: number): void {
    if (e.shiftKey) {
      selection.range(loadedNames, entry.name)
      return
    }
    if (e.ctrlKey || e.metaKey) {
      selection.toggle(entry.name, index)
      return
    }
    // A plain click selects, a double click opens. Same rule as
    // `FileTable.svelte`'s `onRowClick`, so the two views answer the same
    // gesture the same way.
    selection.only(entry.name, index)
  }

  // Same reasoning as FileRow.svelte's checkbox: without an independent
  // toggle target a touch user has no way to build a multi-selection at all,
  // there being no modifier key to hold on a phone.
  function onCheckboxClick(e: MouseEvent, entry: Entry, index: number): void {
    e.stopPropagation()
    selection.toggle(entry.name, index)
  }

  /** The kebab opens the same menu a right-click does, aimed at the same
   *  rows -- one menu definition, one target rule (see `row-actions.ts`). */
  function onKebabClick(e: MouseEvent, entry: Entry): void {
    e.stopPropagation()
    oncontextmenu(entry, e)
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

  /** Menu key / Shift+F10 on the focused card. The card's kebab is not a tab
   *  stop, so this is how a keyboard reaches the menu; it is positioned from
   *  the card's own box since there is no pointer to position it at. */
  function openMenuForFocused(): void {
    const index = selection.state.focused
    if (index === null) return
    const entry = entries[index]
    if (!entry) return
    const el = document.getElementById(domId(entry.name))
    if (!el) return
    const box = el.getBoundingClientRect()
    oncontextmenu(
      entry,
      new MouseEvent('contextmenu', {
        clientX: Math.round(box.left + box.width / 2),
        clientY: Math.round(box.top + box.height / 2)
      })
    )
  }

  function onKeydown(e: KeyboardEvent): void {
    if (total === 0) return

    switch (e.key) {
      case 'ArrowDown':
      case 'ArrowUp': {
        e.preventDefault()
        const from = selection.state.focused ?? 0
        const to = verticalTarget(from, e.key === 'ArrowDown' ? 1 : -1, folderCount, total, columns)
        moveFocus(to - from, e.shiftKey)
        scrollIndexIntoView(selection.state.focused ?? 0)
        break
      }
      case 'ArrowRight':
      case 'ArrowLeft': {
        e.preventDefault()
        moveFocus(e.key === 'ArrowRight' ? 1 : -1, e.shiftKey)
        scrollIndexIntoView(selection.state.focused ?? 0)
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
      case 'ContextMenu':
        e.preventDefault()
        openMenuForFocused()
        break
      case 'F10':
        if (e.shiftKey) {
          e.preventDefault()
          openMenuForFocused()
        }
        break
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

  const renderedNames = $derived(
    new Set(
      [...folderRows, ...fileRows]
        .flatMap((r) => r.cells.map((c) => c.entry?.name))
        .filter((n): n is string => n !== undefined)
    )
  )
  const activeDescendant = $derived(
    focusedName && renderedNames.has(focusedName) ? domId(focusedName) : undefined
  )

  function iconName(entry: Entry): IconName {
    return entry.kind === 'dir' ? 'folder' : isVideoFile(entry.name) ? 'video' : entry.preview?.available ? 'image' : 'file'
  }

  /** Longest edge asked of the server's re-encoder. One value for every
   *  density so a density change doesn't invalidate every cached thumbnail;
   *  the card scales the picture down with `object-fit`. */
  const THUMB_DIM = 512
</script>

<div
  bind:this={viewportEl}
  bind:clientWidth={viewportW}
  class="sc-file-grid"
  class:sc-file-grid--reserve-bar={ui.state.compact}
  class:sc-file-grid--reserve-selection={selection.state.names.size > 0}
  data-density={view.state.density}
  role="grid"
  aria-multiselectable="true"
  aria-rowcount={folderRowCount + fileRowCount}
  aria-colcount={columns}
  aria-label={t('grid.file_grid')}
  aria-activedescendant={activeDescendant}
  aria-busy={loadingMore}
  tabindex="0"
  onkeydown={onKeydown}
>
  {#if total === 0 && !loading}
    <p class="sc-file-grid__empty">{t('common.folder_empty')}</p>
  {:else}
    {#if folderCount > 0}
      <!-- The visible heading is hidden from assistive tech and the same text
           put on the rowgroup instead: a heading is not a legal child of a
           grid, a labelled rowgroup is, and it is what gets announced on the
           way into the section. -->
      <p class="sc-file-grid__group" aria-hidden="true">{t('grid.folders')}</p>
      <div
        bind:this={foldersEl}
        class="sc-file-grid__section"
        role="rowgroup"
        aria-label={t('grid.folders')}
        style:height="{folderWin.totalHeight}px"
      >
        <div class="sc-file-grid__window" style:transform="translate3d(0,{folderWin.padTop}px,0)">
          {#each folderRows as row (row.key)}
            <div
              class="sc-file-grid__row"
              role="row"
              aria-rowindex={row.ariaRow}
              style:height="{CARD.folderH}px"
              style:gap="{CARD.gap}px"
              style:padding-block-end="{CARD.gap}px"
            >
              {#each row.cells as cell, col (cell.entry ? cell.entry.name : `sc-ph-${cell.index}`)}
                {#if cell.entry}
                  {@const entry = cell.entry}
                  <!-- svelte-ignore a11y_click_events_have_key_events, a11y_interactive_supports_focus -->
                  <div
                    id={domId(entry.name)}
                    class="sc-file-grid__card sc-file-grid__card--folder m3-layer"
                    class:sc-file-grid__card--selected={selection.state.names.has(entry.name)}
                    class:sc-file-grid__card--focused={focusedName === entry.name}
                    role="gridcell"
                    aria-colindex={col + 1}
                    aria-selected={selection.state.names.has(entry.name)}
                    style:width="{cardW}px"
                    onclick={(e) => onCardClick(e, entry, cell.index)}
                    ondblclick={() => onopen(entry)}
                    oncontextmenu={(e) => {
                      e.preventDefault()
                      // See FileTable: the page handles blank space.
                      e.stopPropagation()
                      oncontextmenu(entry, e)
                    }}
                  >
                    <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
                    <span class="sc-file-grid__check sc-touch-target" onclick={(e) => onCheckboxClick(e, entry, cell.index)} ondblclick={(e) => e.stopPropagation()}>
                      <!-- Inert input, click owned by the card (see FileRow). -->
                      <Checkbox>
                        <input
                          type="checkbox"
                          checked={selection.state.names.has(entry.name)}
                          tabindex="-1"
                          aria-label={t('common.select', { name: entry.name })}
                          onchange={() => {}}
                        />
                      </Checkbox>
                    </span>
                    <span class="sc-file-grid__type"><Icon icon={icons.folder} size={20} /></span>
                    <!-- The inner `<bdi>` is load-bearing. The name truncates
                         from the front with `direction: rtl`, and that also
                         hands it to the bidi algorithm as RTL paragraph text:
                         `2026-budget.csv` drew as `budget.csv-2026`, reordered
                         rather than truncated. An LTR isolate inside the RTL
                         box is one run in its own direction, so the box still
                         ellipsizes on the left and the name still reads the
                         way it is spelled on disk. -->
                    <span class="sc-file-grid__name"><bdi>{entry.name}</bdi></span>
                    {#if entry.confusable}
                      <span class="sc-file-grid__badge" title={t('common.look_alike_characters')}>
                        <Icon icon={icons.warning} size={14} />
                      </span>
                    {/if}
                    <button
                      type="button"
                      class="sc-file-grid__kebab"
                      tabindex="-1"
                      aria-label={t('grid.more_actions', { name: entry.name })}
                      onclick={(e) => onKebabClick(e, entry)}
                      ondblclick={(e) => e.stopPropagation()}
                    >
                      <Icon icon={icons['more-vert']} size={18} />
                    </button>
                  </div>
                {:else}
                  <div class="sc-file-grid__card sc-file-grid__card--folder sc-file-grid__skeleton" style:width="{cardW}px" aria-busy="true">
                    <span class="sc-file-grid__skeleton-line"></span>
                  </div>
                {/if}
              {/each}
            </div>
          {/each}
        </div>
      </div>
    {/if}

    {#if fileCount > 0}
      <p class="sc-file-grid__group" aria-hidden="true">{t('grid.files')}</p>
      <div
        bind:this={filesEl}
        class="sc-file-grid__section"
        role="rowgroup"
        aria-label={t('grid.files')}
        style:height="{fileWin.totalHeight}px"
      >
        <div class="sc-file-grid__window" style:transform="translate3d(0,{fileWin.padTop}px,0)">
          {#each fileRows as row (row.key)}
            <div
              class="sc-file-grid__row"
              role="row"
              aria-rowindex={row.ariaRow}
              style:height="{CARD.fileH}px"
              style:gap="{CARD.gap}px"
              style:padding-block-end="{CARD.gap}px"
            >
              {#each row.cells as cell, col (cell.entry ? cell.entry.name : `sc-ph-${cell.index}`)}
                {#if cell.entry}
                  {@const entry = cell.entry}
                  <!-- svelte-ignore a11y_click_events_have_key_events, a11y_interactive_supports_focus -->
                  <div
                    id={domId(entry.name)}
                    class="sc-file-grid__card sc-file-grid__card--file m3-layer"
                    class:sc-file-grid__card--selected={selection.state.names.has(entry.name)}
                    class:sc-file-grid__card--focused={focusedName === entry.name}
                    role="gridcell"
                    aria-colindex={col + 1}
                    aria-selected={selection.state.names.has(entry.name)}
                    style:width="{cardW}px"
                    onclick={(e) => onCardClick(e, entry, cell.index)}
                    ondblclick={() => onopen(entry)}
                    oncontextmenu={(e) => {
                      e.preventDefault()
                      e.stopPropagation()
                      oncontextmenu(entry, e)
                    }}
                  >
                    <div class="sc-file-grid__head">
                      <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
                      <span class="sc-file-grid__check sc-touch-target" onclick={(e) => onCheckboxClick(e, entry, cell.index)} ondblclick={(e) => e.stopPropagation()}>
                        <Checkbox>
                          <input
                            type="checkbox"
                            checked={selection.state.names.has(entry.name)}
                            tabindex="-1"
                            aria-label={t('common.select', { name: entry.name })}
                            onchange={() => {}}
                          />
                        </Checkbox>
                      </span>
                      <!-- No type icon here, unlike the folder card. The
                           thumbnail below already shows one whenever there is
                           no picture, and a second copy cost the name a
                           quarter of a 224px header: `...-000023.mp4` was all
                           that survived of a real filename. -->
                      <span class="sc-file-grid__name"><bdi>{entry.name}</bdi></span>
                      {#if entry.confusable}
                        <span class="sc-file-grid__badge" title={t('common.look_alike_characters')}>
                          <Icon icon={icons.warning} size={14} />
                        </span>
                      {/if}
                      <button
                        type="button"
                        class="sc-file-grid__kebab"
                        tabindex="-1"
                        aria-label={t('grid.more_actions', { name: entry.name })}
                        onclick={(e) => onKebabClick(e, entry)}
                      ondblclick={(e) => e.stopPropagation()}
                      >
                        <Icon icon={icons['more-vert']} size={18} />
                      </button>
                    </div>
                    <div class="sc-file-grid__thumb">
                      <Thumbnail {entry} dim={THUMB_DIM} fallback={iconName(entry)} iconSize={40} />
                    </div>
                    <span class="sc-file-grid__meta">{formatBytes(entry.size)}</span>
                  </div>
                {:else}
                  <div class="sc-file-grid__card sc-file-grid__card--file sc-file-grid__skeleton" style:width="{cardW}px" aria-busy="true">
                    <span class="sc-file-grid__skeleton-line"></span>
                    <span class="sc-file-grid__skeleton-block"></span>
                  </div>
                {/if}
              {/each}
            </div>
          {/each}
        </div>
      </div>
    {/if}
  {/if}
</div>

<style>
  .sc-file-grid {
    position: relative;
    flex: 1;
    min-width: 0;
    /* Document-scroll migration: see FileTable.svelte's `.sc-file-table` rule
       for the full reasoning -- this element is no longer its own scroll
       container (no `overflow: auto`), doesn't stretch to fill a now
       content-height ancestor (`align-self: flex-start`), and can't claim
       `contain: strict`'s size containment since it no longer has a definite
       size independent of its (virtualized) content. */
    align-self: flex-start;
    contain: content;
    background: var(--m3c-surface);
    container-type: inline-size;
    container-name: sc-file-grid;
  }
  .sc-file-grid:focus-visible {
    outline: 3px solid var(--m3c-secondary);
    outline-offset: -3px;
  }
  /* Two things are fixed over the bottom of the viewport and would otherwise
     sit on top of the last rows: `NavigationBar` at compact width, and the
     selection bar whenever something is selected. Both reservations live on
     this element rather than on an ancestor because its real (virtualized)
     height escapes past `.sc-app-shell__main`'s own box.

     Padding, not a margin or a scroll, so appearing changes only how far the
     page can scroll and never where a row is. That is the whole point of the
     selection bar being fixed: see `b/[...path]/+page.svelte`. */
  .sc-file-grid {
    padding-bottom: calc(var(--sc-reserve-nav, 0px) + var(--sc-reserve-selection, 0px));
  }
  .sc-file-grid--reserve-bar {
    --sc-reserve-nav: calc(var(--sc-nav-bar-height) + env(safe-area-inset-bottom, 0px));
  }
  .sc-file-grid--reserve-selection {
    --sc-reserve-selection: 80px;
  }
  .sc-file-grid__group {
    @apply --m3-title-small;
    margin: 0;
    padding: 16px 16px 8px;
    color: var(--m3c-on-surface-variant);
  }
  .sc-file-grid__section {
    position: relative;
  }
  .sc-file-grid__window {
    will-change: transform;
    padding-inline: 16px;
  }
  .sc-file-grid__row {
    display: flex;
    align-items: flex-start;
  }
  .sc-file-grid__card {
    position: relative;
    box-sizing: border-box;
    height: 100%;
    border: 1px solid var(--m3c-outline-variant);
    border-radius: var(--m3-shape-medium);
    background: var(--m3c-surface-container-low);
    color: var(--m3c-on-surface);
    cursor: pointer;
    overflow: hidden;
  }
  .sc-file-grid__card--folder {
    display: flex;
    align-items: center;
    gap: 8px;
    padding-inline: 8px;
  }
  .sc-file-grid__card--file {
    display: flex;
    flex-direction: column;
  }
  .sc-file-grid__card--selected {
    background: var(--m3c-secondary-container);
    color: var(--m3c-on-secondary-container);
    border-color: var(--m3c-secondary);
  }
  /* Only while this grid owns focus. See FileTable.svelte's matching rule for
     why an activedescendant indicator must not draw on an unfocused grid. */
  .sc-file-grid:focus .sc-file-grid__card--focused {
    outline: 3px solid var(--m3c-secondary);
    outline-offset: -3px;
  }
  .sc-file-grid__head {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 4px 8px;
    min-height: 40px;
  }
  .sc-file-grid__type {
    display: inline-flex;
    flex: none;
    color: var(--m3c-primary);
  }
  .sc-file-grid__name {
    flex: 1;
    min-width: 0;
    @apply --m3-body-medium;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    /* Truncate filenames from the front so the extension, the part that
       disambiguates, survives. */
    direction: rtl;
    text-align: left;
  }
  /* See the markup: the isolate is what keeps `direction: rtl` truncating the
     name instead of reordering it. */
  .sc-file-grid__name > bdi {
    direction: ltr;
  }
  .sc-file-grid__thumb {
    flex: 1;
    min-height: 0;
    margin: 0 8px;
    border-radius: var(--m3-shape-small);
    background: var(--m3c-surface-container-highest);
    overflow: hidden;
  }
  .sc-file-grid__meta {
    @apply --m3-body-small;
    padding: 4px 8px 8px;
    color: var(--m3c-on-surface-variant);
  }
  .sc-file-grid__card--selected .sc-file-grid__meta {
    color: inherit;
  }
  .sc-file-grid__check {
    display: inline-flex;
    flex: none;
    /* `.sc-touch-target` (app.css) expands the hit area to MD3's 48px minimum
       via an invisible `::before`, without growing the visible glyph. */
  }
  .sc-file-grid__kebab {
    display: inline-flex;
    flex: none;
    align-items: center;
    justify-content: center;
    width: 32px;
    height: 32px;
    padding: 0;
    border: none;
    border-radius: 50%;
    background: none;
    color: inherit;
    cursor: pointer;
  }
  .sc-file-grid__kebab:hover {
    background: color-mix(in srgb, currentColor 12%, transparent);
  }
  .sc-file-grid__badge {
    display: inline-flex;
    flex: none;
    color: var(--m3c-error);
  }
  .sc-file-grid__skeleton {
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding: 8px;
    cursor: default;
  }
  .sc-file-grid__skeleton-line {
    height: 12px;
    border-radius: var(--m3-shape-extra-small);
    background: var(--m3c-surface-container-highest);
    animation: sc-file-grid-pulse 1.2s ease-in-out infinite;
  }
  .sc-file-grid__skeleton-block {
    flex: 1;
    border-radius: var(--m3-shape-small);
    background: var(--m3c-surface-container-highest);
    animation: sc-file-grid-pulse 1.2s ease-in-out infinite;
  }
  @keyframes sc-file-grid-pulse {
    0%,
    100% {
      opacity: 0.5;
    }
    50% {
      opacity: 1;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .sc-file-grid__skeleton-line,
    .sc-file-grid__skeleton-block {
      animation: none;
    }
  }
  .sc-file-grid__empty {
    /* See FileTable.svelte's `.sc-file-table__empty` for why this is a plain
       in-flow block now instead of `position: absolute; inset: 0`. */
    display: flex;
    align-items: center;
    justify-content: center;
    padding-block: 64px;
    color: var(--m3c-on-surface-variant);
  }
</style>
