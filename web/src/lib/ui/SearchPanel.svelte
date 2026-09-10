<script lang="ts">
  // The search experience, once. The desktop top sheet and the phone route
  // both draw this; neither owns any of it.
  //
  // A search here runs to the end. The server streams every match as it finds
  // one, so the list grows while the walk is still going and there is nothing
  // to press for more. What that costs is ordering: matches arrive in the
  // order the tree gives them up, and the sort is applied once the stream
  // finishes, because re-sorting a hundred thousand rows on every arriving
  // batch would spend the whole search sorting.
  import { goto } from '$app/navigation'
  import { api } from '../api/client'
  import type { SearchDone, SearchHit } from '../api/client'
  import { EXTENSION_PRESETS, resolveExtensions } from '../search/filters'
  import { computeWindow } from '../virtual/windowing'
  import { formatBytes } from '../format/bytes'
  import { formatDateNs, t } from '../i18n'
  import { Icon, MenuItem } from 'm3-svelte'
  import { icons } from '../icons'
  import Button from './Button.svelte'
  import Divider from './Divider.svelte'
  import Menu from './Menu.svelte'
  import ProgressCircular from './ProgressCircular.svelte'
  import Select from './Select.svelte'
  import TextField from './TextField.svelte'

  interface Props {
    /** The folder the search started from. Ranks that subtree up; it never
     *  narrows the search, which always covers everything reachable. */
    scope?: string
    autofocus?: boolean
    /** A result was opened, so a surface drawn over something else can go
     *  away before the navigation lands. */
    onnavigated?: () => void
    /** Rendered at the end of the query row: the sheet puts its close button
     *  there, the page puts nothing. */
    trailing?: import('svelte').Snippet
  }

  let { scope = '', autofocus = false, onnavigated, trailing }: Props = $props()

  type Kind = 'any' | 'file' | 'dir'
  type SortKey = 'relevance' | 'name' | 'size' | 'date'

  let query = $state('')
  let kind = $state<Kind>('any')
  let presets = $state<readonly string[]>([])
  let extText = $state('')
  let sortKey = $state('relevance')

  /** `$state.raw`: the list is replaced wholesale on every flush, and a deep
   *  proxy over a hundred thousand hits costs on every read. */
  let hits = $state.raw<readonly SearchHit[]>([])
  let running = $state(false)
  /** A query has actually been submitted, so an empty list means "nothing
   *  matched" rather than "not asked yet". */
  let ran = $state(false)
  let failure = $state<string | null>(null)
  let elapsedMs = $state<number | null>(null)

  let cancel: (() => void) | null = null
  /** Hits arrive one frame at a time; rendering per hit would spend the
   *  search re-rendering, so they land here and go in as batches. */
  let arriving: SearchHit[] = []
  let flushTimer: ReturnType<typeof setTimeout> | null = null

  const FLUSH_MS = 100
  const ROW_PX = 48

  function flush(): void {
    if (flushTimer !== null) {
      clearTimeout(flushTimer)
      flushTimer = null
    }
    if (arriving.length === 0) return
    hits = [...hits, ...arriving]
    arriving = []
  }

  function scheduleFlush(): void {
    if (flushTimer !== null) return
    flushTimer = setTimeout(() => {
      flushTimer = null
      flush()
    }, FLUSH_MS)
  }

  /** The framework owns the `<input>`, so focus reaches it through the
   *  wrapper rather than a bound element, the same way TextField's own
   *  autofocus does. */
  export function focus(): void {
    fieldEl?.querySelector('input')?.focus()
  }

  function start(): void {
    cancel?.()
    cancel = null
    arriving = []
    hits = []
    failure = null
    elapsedMs = null
    scrollTop = 0
    listEl?.scrollTo({ top: 0 })

    const text = query.trim()
    if (text === '') {
      ran = false
      running = false
      return
    }

    ran = true
    running = true
    const exts = resolveExtensions(presets, extQuery)
    cancel = api.searchStream(
      { query: text, kind: kind === 'any' ? undefined : kind, exts, scope: scope || undefined },
      (hit: SearchHit) => {
        arriving.push(hit)
        scheduleFlush()
      },
      (done: SearchDone) => {
        flush()
        running = false
        cancel = null
        elapsedMs = done.elapsedMs ?? null
        failure = done.error ?? null
      }
    )
  }

  /** Stops the walk where it is. What arrived stays on screen and says it is
   *  partial, which is the honest reading of a search someone interrupted. */
  function stop(): void {
    if (!running) return
    cancel?.()
    cancel = null
    flush()
    running = false
    failure = 'stopped'
  }

  /** Back to an empty search, filters included: a leftover extension is the
   *  quiet reason a later query finds nothing. */
  function clear(): void {
    cancel?.()
    cancel = null
    arriving = []
    hits = []
    running = false
    ran = false
    failure = null
    query = ''
    kind = 'any'
    presets = []
    extText = ''
    extQuery = ''
    focus()
  }

  function togglePreset(id: string): void {
    presets = presets.includes(id) ? presets.filter((p) => p !== id) : [...presets, id]
  }

  function setKind(next: Kind): void {
    kind = next
    if (next === 'dir') {
      // A folder carries no extension, so the pair matches nothing and the
      // server refuses it. Dropping the type filter here is what the person
      // asking for folders meant.
      presets = []
      extText = ''
      extQuery = ''
    }
  }

  /** The typed extensions as last committed. A search here walks whole trees,
   *  so it runs when the box is left or Enter is pressed, not on the way to
   *  spelling "pdf". */
  let extQuery = $state('')
  function commitExts(): void {
    extQuery = extText
  }

  // A filter changed after a search ran, so the answer on screen is for the
  // old question. Asking again is what the person just requested.
  let lastFilters = $state('')
  $effect(() => {
    const now = `${kind}|${presets.join(',')}|${extQuery.trim().toLowerCase()}`
    if (now === lastFilters) return
    const first = lastFilters === ''
    lastFilters = now
    if (!first && ran) start()
  })

  $effect(() => () => {
    cancel?.()
    if (flushTimer !== null) clearTimeout(flushTimer)
  })

  function sorted(list: readonly SearchHit[], key: SortKey): readonly SearchHit[] {
    if (key === 'relevance') return list
    const out = [...list]
    switch (key) {
      case 'name':
        out.sort((a, b) => a.entry.name.localeCompare(b.entry.name))
        break
      case 'size':
        out.sort((a, b) => b.entry.size - a.entry.size)
        break
      case 'date':
        out.sort((a, b) => Number(BigInt(b.entry.mtime_ns || '0') - BigInt(a.entry.mtime_ns || '0')))
        break
    }
    return out
  }

  // Ordering waits for the end of the stream. The server hands over matches
  // scored and grouped by folder, so mid-search the arrival order is the only
  // stable one there is; sorting each batch would reshuffle the rows under a
  // reader on every flush.
  const view = $derived(running ? hits : sorted(hits, sortKey as SortKey))

  const dirCount = $derived(view.filter((h) => h.entry.kind === 'dir').length)

  /** How many filters are on, which is what the button says when it is not
   *  wide enough to list them. */
  const activeCount = $derived((kind === 'any' ? 0 : 1) + presets.length + (extQuery.trim() === '' ? 0 : 1))

  let filtersOpen = $state(false)
  let filtersX = $state(0)
  let filtersY = $state(0)
  let filtersMenuEl: HTMLDivElement | undefined = $state()
  let filtersTrigger: HTMLElement | null = null

  function openFilters(e: MouseEvent): void {
    if (filtersOpen) {
      closeFilters()
      return
    }
    e.stopPropagation()
    const btn = e.currentTarget as HTMLElement
    filtersTrigger = btn
    const rect = btn.getBoundingClientRect()
    filtersX = rect.left
    filtersY = rect.bottom + 4
    filtersOpen = true
    queueMicrotask(() => filtersMenuEl?.querySelector<HTMLButtonElement>('button')?.focus())
  }

  // Escape belongs to the menu while the menu is open. Without this the key
  // reached the sheet's own dialog underneath and closed the whole search.
  function onKeydownCapture(e: KeyboardEvent): void {
    if (!filtersOpen || e.key !== 'Escape') return
    e.preventDefault()
    e.stopPropagation()
    closeFilters()
  }

  function closeFilters(): void {
    filtersOpen = false
    filtersTrigger?.focus()
    filtersTrigger = null
  }

  /** What is narrowing the search right now, in the words the controls use. */
  const activeFilters = $derived.by(() => {
    const parts: string[] = []
    if (kind === 'file') parts.push(t('search.kind_file'))
    if (kind === 'dir') parts.push(t('search.kind_dir'))
    const exts = resolveExtensions(presets, extQuery)
    if (exts.length > 0) parts.push(exts.join(', '))
    return parts.join(' + ')
  })

  let fieldEl: HTMLDivElement | undefined = $state()
  let listEl: HTMLDivElement | undefined = $state()
  let scrollTop = $state(0)
  let viewportPx = $state(480)

  const window_ = $derived(
    computeWindow({ scrollTop, viewportHeight: viewportPx, rowHeight: ROW_PX, itemCount: view.length, overscan: 8 })
  )
  const rows = $derived(view.slice(window_.start, window_.end))

  $effect(() => {
    const el = listEl
    if (!el || typeof ResizeObserver === 'undefined') return
    const ro = new ResizeObserver(() => {
      viewportPx = el.clientHeight
    })
    ro.observe(el)
    viewportPx = el.clientHeight
    return () => ro.disconnect()
  })

  function onListScroll(): void {
    if (listEl) scrollTop = listEl.scrollTop
  }

  function onQueryKeydown(e: KeyboardEvent): void {
    if (e.key === 'Enter') {
      e.preventDefault()
      start()
    }
  }

  /** The folder a hit lives in, which is the part of a path a name does not
   *  already say. */
  function folderOf(path: string): string {
    const cut = path.lastIndexOf('/')
    return cut <= 0 ? '/' : path.slice(0, cut)
  }

  function open(hit: SearchHit): void {
    onnavigated?.()
    // A folder opens; a file opens the folder holding it, since there is
    // nowhere else to land.
    const target = hit.entry.kind === 'dir' ? hit.path : folderOf(hit.path)
    void goto(`/b${target}`)
  }

  const statusText = $derived.by(() => {
    if (running) return t('search.searching', { count: hits.length })
    if (!ran) return ''
    if (failure === 'stopped') return t('search.stopped', { count: view.length })
    if (failure === 'busy') return t('search.busy')
    if (failure === 'network') return t('search.connection_lost', { count: view.length })
    if (failure) return t('search.failed')
    if (elapsedMs !== null) return t('search.found_in', { count: view.length, ms: elapsedMs })
    return t('search.found', { count: view.length })
  })
</script>

<svelte:window onkeydowncapture={onKeydownCapture} />

<div class="sc-search">
  <div class="sc-search__query">
    <span class="sc-search__query-icon" aria-hidden="true"><Icon icon={icons.search} /></span>
    <div class="sc-search__field" bind:this={fieldEl}>
      <TextField
        bind:value={query}
        type="search"
        label={t('search.placeholder')}
        {autofocus}
        onkeydown={onQueryKeydown}
      />
    </div>
    <Button variant="filled" onclick={start}>{t('search.run')}</Button>
    {#if ran}
      <!-- Worded, not a second ✕: the field already carries the browser's
           own clear affordance and the sheet its close button, and three
           crosses in a row say nothing about which does what. -->
      <Button variant="text" onclick={clear}>{t('search.clear')}</Button>
    {/if}
    {@render trailing?.()}
  </div>

  <div class="sc-search__filters">
    <!-- One button rather than a row of chips: the type list alone is six of
         them, and they pushed the results down the panel to say what was
         mostly "no filter". What is on is still visible beside the button. -->
    <Button variant="outlined" onclick={openFilters} ariaLabel={t('search.filters')}>
      {#snippet icon()}<Icon icon={icons.filter} size={18} />{/snippet}
      {activeCount === 0 ? t('search.filters') : t('search.filters_active', { count: activeCount })}
    </Button>
    {#if activeFilters !== ''}
      <span class="sc-search__active">{activeFilters}</span>
    {/if}

    <div class="sc-search__exts" onfocusout={commitExts}>
      <!-- Committed on Enter or on leaving the box, not per keystroke:
           every commit starts a walk of every share, so "pdf" typed a
           letter at a time would start three of them. -->
      <TextField
        bind:value={extText}
        label={t('search.extensions')}
        placeholder="pdf, hwp, zip"
        onkeydown={(e) => {
          if (e.key === 'Enter') commitExts()
        }}
      />
    </div>
  </div>

  <Menu open={filtersOpen} onclose={closeFilters} x={filtersX} y={filtersY} align="start">
    <div bind:this={filtersMenuEl} role="none">
      {#each [['any', t('search.kind_any')], ['file', t('search.kind_file')], ['dir', t('search.kind_dir')]] as const as [id, label] (id)}
        <MenuItem icon={kind === id ? icons.check : 'space'} onclick={() => setKind(id as Kind)}>
          {kind === id ? t('search.filter_selected', { label }) : label}
        </MenuItem>
      {/each}
      <Divider />
      <!-- The type rows are gone rather than greyed out while folders are the
           target: a folder has no extension, so each of them would narrow the
           answer to nothing. -->
      {#if kind === 'dir'}
        <p class="sc-search__hint">{t('search.folders_have_no_extension')}</p>
      {:else}
        {#each EXTENSION_PRESETS as preset (preset.id)}
          <MenuItem
            icon={presets.includes(preset.id) ? icons.check : 'space'}
            onclick={() => togglePreset(preset.id)}
          >
            {presets.includes(preset.id)
              ? t('search.filter_selected', { label: t(preset.labelKey) })
              : t(preset.labelKey)}
          </MenuItem>
        {/each}
      {/if}
    </div>
  </Menu>

  <div class="sc-search__status">
    <!-- The running count changes ten times a second, and a live region
         reading it announces nothing else for the length of the search. The
         count is shown; only the outcome is spoken. -->
    <span class="sc-search__count" aria-hidden={running}>
      {#if running}
        <ProgressCircular size={16} label={t('search.searching_label')} />
      {/if}
      {statusText}
    </span>
    <span class="sc-search__spoken" role="status" aria-live="polite">
      {running ? '' : statusText}
    </span>
    <span class="sc-search__status-actions">
      {#if running}
        <Button variant="text" onclick={stop}>{t('search.stop')}</Button>
      {/if}
      {#if ran && view.length > 0}
        <div class="sc-search__sort">
          <Select
            bind:value={sortKey}
            label={t('search.sort')}
            options={[
              { value: 'relevance', text: t('search.sort_relevance') },
              { value: 'name', text: t('search.sort_name') },
              { value: 'size', text: t('search.sort_size') },
              { value: 'date', text: t('search.sort_date') }
            ]}
          />
        </div>
      {/if}
    </span>
  </div>

  <div class="sc-search__results" bind:this={listEl} onscroll={onListScroll} tabindex="-1">
    {#if !ran}
      <p class="sc-search__note">{t('search.type_and_press_enter')}</p>
    {:else if view.length === 0 && !running}
      <p class="sc-search__note">
        {t('search.no_results')}
        <!-- Naming the filter that is on: an extension left over from the
             previous query is the reason a search finds nothing while
             looking like it searched for everything. -->
        {#if activeFilters !== ''}
          <span class="sc-search__hint">{t('search.filtered_by', { filters: activeFilters })}</span>
        {/if}
      </p>
    {:else}
      <div class="sc-search__spacer" style="height: {window_.totalHeight}px">
        <ul class="sc-search__rows" style="transform: translate3d(0, {window_.padTop}px, 0)">
          {#each rows as hit (hit.path)}
            <li>
              <button class="sc-search__row" type="button" onclick={() => open(hit)}>
                <Icon icon={icons[hit.entry.kind === 'dir' ? 'folder' : 'file']} size={20} />
                <span class="sc-search__name">{hit.entry.name}</span>
                <span class="sc-search__folder">{folderOf(hit.path)}</span>
                <span class="sc-search__size">
                  {hit.entry.kind === 'dir' ? '' : formatBytes(hit.entry.size)}
                </span>
                <span class="sc-search__date">{formatDateNs(hit.entry.mtime_ns)}</span>
              </button>
            </li>
          {/each}
        </ul>
      </div>
    {/if}
  </div>

  {#if ran && view.length > 0}
    <p class="sc-search__summary">
      {t('search.summary', { files: view.length - dirCount, folders: dirCount })}
    </p>
  {/if}
</div>

<style>
  .sc-search {
    display: flex;
    flex-direction: column;
    gap: 12px;
    min-height: 0;
    height: 100%;
  }

  .sc-search__query {
    display: flex;
    align-items: center;
    gap: 8px;
  }

  .sc-search__query-icon {
    display: flex;
    color: var(--m3c-on-surface-variant, currentColor);
  }

  .sc-search__field {
    flex: 1 1 auto;
    min-width: 0;
  }

  .sc-search__filters {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px;
  }

  .sc-search__active {
    flex: 1 1 auto;
    min-width: 0;
    overflow: hidden;
    color: var(--m3c-on-surface-variant, inherit);
    font: var(--m3-font-body-small);
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .sc-search__exts {
    /* Sized to the list it holds rather than the row it sits in: stretched
       across the remaining width it read as the main input. */
    flex: 0 1 240px;
    min-width: 160px;
  }

  .sc-search__status {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    min-height: 40px;
  }

  /* Off-screen but readable: the outcome is announced once, while the
     running count stays visual only. */
  .sc-search__spoken {
    position: absolute;
    width: 4px;
    height: 4px;
    margin: -4px;
    padding: 0;
    overflow: hidden;
    clip-path: inset(50%);
    white-space: nowrap;
  }

  .sc-search__hint {
    margin: 0;
    color: var(--m3c-on-surface-variant, inherit);
    font: var(--m3-font-body-small);
  }

  .sc-search__count {
    display: flex;
    align-items: center;
    gap: 8px;
    font: var(--m3-font-body-medium);
    color: var(--m3c-on-surface-variant, inherit);
  }

  .sc-search__status-actions {
    display: flex;
    align-items: center;
    gap: 8px;
  }

  .sc-search__sort {
    min-width: 160px;
  }

  .sc-search__results {
    flex: 1 1 auto;
    min-height: 240px;
    overflow: auto;
    border-radius: 12px;
    background: var(--m3c-surface-container-low, transparent);
  }

  .sc-search__spacer {
    position: relative;
    width: 100%;
  }

  .sc-search__rows {
    position: absolute;
    inset-inline: 0;
    top: 0;
    margin: 0;
    padding: 0;
    list-style: none;
    will-change: transform;
  }

  .sc-search__row {
    display: grid;
    grid-template-columns: 20px minmax(0, 2fr) minmax(0, 3fr) 96px auto;
    align-items: center;
    gap: 12px;
    width: 100%;
    height: 48px;
    padding-inline: 12px;
    border: none;
    background: none;
    color: inherit;
    font: var(--m3-font-body-medium);
    text-align: start;
    cursor: pointer;
  }

  .sc-search__row:hover,
  .sc-search__row:focus-visible {
    background: var(--m3c-surface-container-high, rgb(0 0 0 / 6%));
  }

  .sc-search__name,
  .sc-search__folder {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .sc-search__folder,
  .sc-search__size,
  .sc-search__date {
    color: var(--m3c-on-surface-variant, inherit);
    font: var(--m3-font-body-small);
    /* A wrapped date would push the row past the fixed height the virtual
       list measures every row by, and the window maths would drift. */
    white-space: nowrap;
    overflow: hidden;
  }

  .sc-search__size {
    text-align: end;
    white-space: nowrap;
  }

  .sc-search__note,
  .sc-search__summary {
    margin: 0;
    padding: 16px;
    color: var(--m3c-on-surface-variant, inherit);
    font: var(--m3-font-body-medium);
  }

  .sc-search__summary {
    padding-block: 0;
    padding-inline: 4px;
    font: var(--m3-font-body-small);
  }

  @media (max-width: 840px) {
    .sc-search__row {
      grid-template-columns: 20px minmax(0, 1fr) 80px;
    }

    .sc-search__folder,
    .sc-search__date {
      display: none;
    }
  }
</style>
