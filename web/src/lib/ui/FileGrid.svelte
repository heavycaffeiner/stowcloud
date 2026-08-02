<script lang="ts">
  // FileGrid.svelte — virtual-scrolled thumbnail/icon grid view.
  // lists `FileGrid` alongside `FileTable` in the
  // app-specific component inventory; §5 says the grid view "uses the same
  // technique with a fixed cell size" as the virtualized table. This reuses
  // `computeWindow` (built for 1D rows) by treating each *visual grid row* of
  // `columns` cells as one "row" of the windowing maths — `rowHeight` becomes
  // the cell's height, `itemCount` becomes the number of grid rows, and each
  // rendered grid row is expanded back into up to `columns` file cells.
  //
  // Same accessibility contract as FileTable: one tab stop (`tabindex=0` on
  // the grid container), `aria-activedescendant` tracks the "virtually
  // focused" cell, and per-cell tabindex is intentionally never given
  // ( — a 100k-entry directory cannot have 100k tab stops).
  import { t } from '../i18n'
  import type { Entry } from '../api/client'
  import type { BrowseState } from '../state/browse.svelte'
  import { uiState } from '../state/ui.svelte'
  import { formatBytes } from '../format/bytes'
  import {
    computeScaleMapping,
    computeWindow,
    documentScrollTop,
    effectiveViewportHeight,
    rowIndexToScrollTop
  } from '../virtual/windowing'
  import { Checkbox, Icon } from 'm3-svelte'
  import { icons, type IconName } from '../icons'

  interface Props {
    browse: BrowseState
    onopen: (entry: Entry) => void
    oncontextmenu: (entry: Entry, e: MouseEvent) => void
    onrename?: () => void
    ondelete?: () => void
    onsearchfocus?: () => void
  }

  let { browse, onopen, oncontextmenu, onrename, ondelete, onsearchfocus }: Props = $props()

  // 4px-grid cell sizes, keyed by the same density the toolbar's "density"
  // control already drives for FileTable's row height.
  const CELL = $derived(
    { compact: { w: 96, h: 112 }, comfortable: { w: 116, h: 136 }, spacious: { w: 140, h: 160 } }[browse.density]
  )
  const CELL_GAP = 8 // 8px
  const OVERSCAN = 3 // grid rows, not individual cells

  // Document-scroll migration: see the matching comment block in
  // FileTable.svelte for the full "why" (the short version: a browser only
  // collapses its chrome when the document scrolls, and this element used to
  // be its own `overflow: auto` box, which is exactly what prevented that).
  // `viewportW` stays a real `bind:clientWidth` -- this element still has a
  // genuine, bounded *width* (that never changed); only its height became
  // content-driven instead of viewport-bounded, which is why only the
  // height-derived numbers (`scrollTop`/`viewportH`) move to window/
  // visualViewport reads below.
  let viewportEl: HTMLDivElement | undefined = $state()
  let viewportW = $state(0)
  let viewportDocumentTop = $state(0)
  let scrollTop = $state(0)
  let viewportH = $state(0)

  function measure(): void {
    if (!viewportEl) return
    viewportDocumentTop = viewportEl.getBoundingClientRect().top + window.scrollY
    scrollTop = documentScrollTop(window.scrollY, viewportDocumentTop)
    viewportH = effectiveViewportHeight(window.visualViewport?.height, window.innerHeight)
  }

  $effect(() => {
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
    // Re-measure on mount and whenever the directory changes -- see
    // FileTable.svelte's identical effect for why (content above this
    // element can change height between folders without a scroll/resize
    // event of its own).
    browse.path
    measure()
  })

  const columns = $derived(Math.max(1, Math.floor((viewportW + CELL_GAP) / (CELL.w + CELL_GAP))))
  const cellRowHeight = $derived(CELL.h + CELL_GAP)
  const gridRowCount = $derived(browse.total > 0 ? Math.ceil(browse.total / columns) : 0)

  const win = $derived(
    computeWindow({
      scrollTop,
      viewportHeight: viewportH,
      rowHeight: cellRowHeight,
      itemCount: gridRowCount,
      overscan: OVERSCAN
    })
  )

  interface Cell {
    index: number
    entry: Entry | undefined
  }
  interface GridRow {
    key: string
    cells: Cell[]
  }
  const rows = $derived.by((): GridRow[] => {
    const out: GridRow[] = []
    for (let r = win.start; r < win.end; r++) {
      const cells: Cell[] = []
      const base = r * columns
      for (let c = 0; c < columns; c++) {
        const index = base + c
        if (index >= browse.total) break
        cells.push({ index, entry: browse.rowAt(index) })
      }
      out.push({ key: `sc-grid-row-${r}`, cells })
    }
    return out
  })

  function domId(name: string): string {
    return `sc-grid-cell-${encodeURIComponent(name).replace(/%/g, '_')}`
  }

  $effect(() => {
    // Same debounced-window contract as FileTable — only the visible grid
    // rows' worth of entries are requested, translated back into a flat
    // index range.
    browse.scheduleWindow(win.start * columns, Math.min(browse.total, win.end * columns))
  })

  function scrollRowIntoView(gridRow: number): void {
    if (!viewportEl) return
    // See FileTable.svelte's `scrollRowIntoView` for why this scrolls the
    // window (document coordinates) instead of `viewportEl.scrollTop` (this
    // element isn't a scroll container anymore).
    const mapping = computeScaleMapping(gridRowCount, cellRowHeight)
    const rowTop = rowIndexToScrollTop(gridRow, mapping, cellRowHeight)
    const rowBottom = rowIndexToScrollTop(gridRow + 1, mapping, cellRowHeight)
    if (rowTop < scrollTop) {
      window.scrollTo({ top: viewportDocumentTop + rowTop })
    } else if (rowBottom > scrollTop + viewportH) {
      window.scrollTo({ top: viewportDocumentTop + rowBottom - viewportH })
    }
  }

  function onCellClick(e: MouseEvent, entry: Entry): void {
    if (e.shiftKey) {
      browse.selectRangeTo(entry)
      return
    }
    if (e.ctrlKey || e.metaKey) {
      browse.toggle(entry)
      return
    }
    // See FileTable.svelte's `onRowClick` for why compact width branches
    // here: no modifier key exists on a tap, so a plain tap has to open
    // (nothing selected) or extend the selection (already in selection
    // mode) instead of always replacing it down to one item.
    if (uiState.compact) {
      if (browse.selection.size > 0) browse.toggle(entry)
      else onopen(entry)
      return
    }
    browse.selectOnly(entry)
  }

  // Same reasoning as FileRow.svelte's checkbox: a plain click/tap on the
  // cell (shift/ctrl aside) runs `selectOnly`, so without an independent
  // toggle target a touch user has no way to build a multi-selection at
  // all -- there is no modifier key to hold on a phone. FileGrid never had a
  // checkbox before (unlike FileRow); this is the same fix applied to the
  // view that was missing the affordance entirely, not just the wiring.
  function onCheckboxClick(e: MouseEvent, entry: Entry): void {
    e.stopPropagation()
    browse.toggle(entry)
  }

  function onKeydown(e: KeyboardEvent): void {
    if (browse.total === 0) return

    switch (e.key) {
      case 'ArrowDown':
      case 'ArrowUp': {
        e.preventDefault()
        browse.moveFocus(e.key === 'ArrowDown' ? columns : -columns, e.shiftKey)
        scrollRowIntoView(Math.floor((browse.focusedIndex ?? 0) / columns))
        break
      }
      case 'ArrowRight':
      case 'ArrowLeft': {
        e.preventDefault()
        browse.moveFocus(e.key === 'ArrowRight' ? 1 : -1, e.shiftKey)
        scrollRowIntoView(Math.floor((browse.focusedIndex ?? 0) / columns))
        break
      }
      case ' ': {
        e.preventDefault()
        if (browse.focusedIndex !== null) {
          const entry = browse.rowAt(browse.focusedIndex)
          if (entry) browse.toggle(entry)
        }
        break
      }
      case 'a':
      case 'A':
        if (e.ctrlKey || e.metaKey) {
          e.preventDefault()
          browse.selectAll()
        }
        break
      case 'Enter': {
        const entry = browse.focusedIndex !== null ? browse.rowAt(browse.focusedIndex) : undefined
        if (entry) onopen(entry)
        break
      }
      case 'F2':
        e.preventDefault()
        onrename?.()
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
    browse.focusedName && rows.some((r) => r.cells.some((c) => c.entry?.name === browse.focusedName))
      ? domId(browse.focusedName)
      : undefined
  )

  function iconName(entry: Entry): IconName {
    return entry.kind === 'dir' ? 'folder' : entry.preview?.available ? 'image' : 'file'
  }
</script>

<div
  bind:this={viewportEl}
  bind:clientWidth={viewportW}
  class="sc-file-grid"
  class:sc-file-grid--reserve-bar={uiState.compact}
  data-density={browse.density}
  role="grid"
  aria-multiselectable="true"
  aria-rowcount={gridRowCount}
  aria-colcount={columns}
  aria-label={t('grid.file_grid')}
  aria-activedescendant={activeDescendant}
  aria-busy={browse.loadingWindow}
  tabindex="0"
  onkeydown={onKeydown}
>
  {#if browse.total === 0 && !browse.loading}
    <p class="sc-file-grid__empty">{t('common.folder_empty')}</p>
  {:else}
  <div class="sc-file-grid__spacer" style:height="{win.totalHeight}px">
    <div class="sc-file-grid__window" style:transform="translate3d(0,{win.padTop}px,0)">
      {#each rows as row (row.key)}
        <div class="sc-file-grid__row" role="row" style:height="{CELL.h}px" style:gap="{CELL_GAP}px">
          {#each row.cells as cell (cell.entry ? cell.entry.name : `sc-ph-${cell.index}`)}
            {#if cell.entry}
              {@const entry = cell.entry}
              <!-- svelte-ignore a11y_click_events_have_key_events, a11y_interactive_supports_focus -->
              <div
                id={domId(entry.name)}
                class="sc-file-grid__cell m3-layer"
                class:sc-file-grid__cell--selected={browse.selection.has(entry.name)}
                class:sc-file-grid__cell--focused={browse.focusedName === entry.name}
                role="gridcell"
                aria-selected={browse.selection.has(entry.name)}
                style:width="{CELL.w}px"
                onclick={(e) => onCellClick(e, entry)}
                ondblclick={() => onopen(entry)}
                oncontextmenu={(e) => {
                  e.preventDefault()
                  oncontextmenu(entry, e)
                }}
              >
                <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
                <span class="sc-file-grid__check sc-touch-target" onclick={(e) => onCheckboxClick(e, entry)}>
                  <!-- Inert input, click owned by the cell — see FileRow. -->
                  <Checkbox>
                    <input
                      type="checkbox"
                      checked={browse.selection.has(entry.name)}
                      tabindex="-1"
                      aria-label={t('common.select', { name: entry.name })}
                      onchange={() => {}}
                    />
                  </Checkbox>
                </span>
                <span class="sc-file-grid__icon"><Icon icon={icons[iconName(entry)]} size={32} /></span>
                <span class="sc-file-grid__name">{entry.name}</span>
                <span class="sc-file-grid__meta">{entry.kind === 'dir' ? '—' : formatBytes(entry.size)}</span>
                {#if entry.confusable}
                  <span class="sc-file-grid__badge" title={t('common.look_alike_characters')}><Icon icon={icons.warning} size={14} /></span>
                {/if}
              </div>
            {:else}
              <div class="sc-file-grid__cell sc-file-grid__cell--skeleton" style:width="{CELL.w}px" aria-busy="true">
                <span class="sc-file-grid__skeleton-icon"></span>
                <span class="sc-file-grid__skeleton-name"></span>
              </div>
            {/if}
          {/each}
        </div>
      {/each}
    </div>
  </div>
  {/if}
</div>

<style>
  .sc-file-grid {
    position: relative;
    flex: 1;
    min-width: 0;
    /* Document-scroll migration: see FileTable.svelte's `.sc-file-table`
       rule for the full reasoning -- this element is no longer its own
       scroll container (no `overflow: auto`), doesn't stretch to fill a
       now content-height ancestor (`align-self: flex-start`), and can't
       claim `contain: strict`'s `size` containment since it no longer has
       a definite size independent of its (virtualized) content. */
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
  .sc-file-grid--reserve-bar {
    /* See FileTable.svelte's identical `.sc-file-table--reserve-bar` rule
       for the full reasoning: `NavigationBar` is `position: fixed` and this
       element's real height escapes past `.sc-app-shell__main`'s own box
       (whose `padding-bottom` reservation therefore never reaches here), so
       the bar-height reservation has to live directly on the element whose
       box actually reflects the full (virtualized) content height. */
    padding-bottom: calc(var(--sc-nav-bar-height) + env(safe-area-inset-bottom, 0px));
  }
  .sc-file-grid__window {
    will-change: transform;
    padding-inline: 16px;
  }
  .sc-file-grid__row {
    display: flex;
    align-items: flex-start;
    padding-block-end: 8px;
  }
  .sc-file-grid__cell {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 4px;
    padding: 8px;
    border-radius: var(--m3-shape-medium);
    cursor: pointer;
    color: var(--m3c-on-surface);
    text-align: center;
    position: relative;
  }
  .sc-file-grid__cell--selected {
    background: var(--m3c-secondary-container);
    color: var(--m3c-on-secondary-container);
  }
  /* Only while this grid owns focus — see FileTable.svelte's matching rule
     for why an activedescendant indicator must not draw on an unfocused grid. */
  .sc-file-grid:focus .sc-file-grid__cell--focused {
    outline: 3px solid var(--m3c-secondary);
    outline-offset: -3px;
  }
  .sc-file-grid__icon {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 64px;
    height: 64px;
    color: var(--m3c-primary);
  }
  .sc-file-grid__name {
    width: 100%;
    @apply --m3-body-medium;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    /*: truncate filenames from the front so the
       extension (the part that disambiguates) survives. */
    direction: rtl;
    text-align: center;
    overflow-wrap: anywhere;
  }
  .sc-file-grid__meta {
    @apply --m3-body-small;
    color: var(--m3c-on-surface-variant);
  }
  .sc-file-grid__cell--selected .sc-file-grid__meta {
    color: inherit;
  }
  .sc-file-grid__badge {
    position: absolute;
    top: 4px;
    right: 4px;
    display: inline-flex;
    color: var(--m3c-error);
  }
  .sc-file-grid__check {
    position: absolute;
    top: 4px;
    left: 4px;
    display: inline-flex;
    /* `.sc-touch-target` (app.css) expands the *hit area* to MD3's 48px
       minimum via an invisible `::before`, without growing the visible
       checkbox glyph -- same utility IconButton/Switch/Checkbox already use. */
  }
  .sc-file-grid__cell--skeleton {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 8px;
    padding: 8px;
  }
  .sc-file-grid__skeleton-icon {
    width: 64px;
    height: 64px;
    border-radius: var(--m3-shape-medium);
    background: var(--m3c-surface-container-highest);
    animation: sc-file-grid-pulse 1.2s ease-in-out infinite;
  }
  .sc-file-grid__skeleton-name {
    width: 80%;
    height: 12px;
    border-radius: var(--m3-shape-extra-small);
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
    .sc-file-grid__skeleton-icon,
    .sc-file-grid__skeleton-name {
      animation: none;
    }
  }
  .sc-file-grid__empty {
    /* See FileTable.svelte's `.sc-file-table__empty` for why this is a
       plain in-flow block now instead of `position: absolute; inset: 0`. */
    display: flex;
    align-items: center;
    justify-content: center;
    padding-block: 64px;
    color: var(--m3c-on-surface-variant);
  }
</style>
