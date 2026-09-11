<script lang="ts">
  // The search experience, once. The desktop top sheet and the phone route
  // both draw this; neither owns any of it.
  //
  // A search streams matches as the server walks every accessible folder,
  // subject to bounded work limits. A completed walk is distinct from a
  // server-truncated walk: both preserve the hits that arrived, but only the
  // former can claim the answer is complete. Ordering is applied once the
  // stream finishes, because re-sorting a hundred thousand rows on every
  // arriving batch would spend the whole search sorting.
  import { goto } from '$app/navigation'
  import { api } from '../api/client'
  import type { SearchDone, SearchHit, SearchProgress } from '../api/client'
  import { EXTENSION_PRESETS, parseExtensions, resolveExtensions } from '../search/filters'
  import { computeWindow } from '../virtual/windowing'
  import { formatBytes } from '../format/bytes'
  import { formatDateNs, formatNumber, t } from '../i18n'
  import { search, type SearchSnapshot } from '../store/search.store'
  import { ConnectedButtons, Icon, MenuItem } from 'm3-svelte'
  import { icons } from '../icons'
  import Button from './Button.svelte'
  import Chip from './Chip.svelte'
  import Menu from './Menu.svelte'
  import ProgressCircular from './ProgressCircular.svelte'
  import TextField from './TextField.svelte'

  interface Props {
    /** The folder the search started from. Ranks that subtree up; it never
     *  narrows the search, which always covers everything. */
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

  // A result route unmounts this component on compact screens. Restore only
  // the same ranking scope: otherwise a newly opened search must not display
  // an answer scored for a different current folder. This is intentionally a
  // mount snapshot: when a user changes scope at runtime, the search handlers
  // below update local state explicitly rather than deriving every field from
  // a reactive snapshot.
  function snapshotAtMount(scopeAtMount: string): SearchSnapshot | null {
    const saved = search.peek().snapshot
    return saved?.scope === scopeAtMount ? saved : null
  }
  // Intentional mount snapshot: `scope` selects initial restoration only;
  // subsequent route changes use explicit state updates instead of re-deriving
  // each draft field.
  // svelte-ignore state_referenced_locally
  const restored = snapshotAtMount(scope)

  let query = $state(restored?.query ?? '')
  let kind = $state<Kind>(restored?.kind ?? 'any')
  let presets = $state<readonly string[]>(restored ? [...restored.presets] : [])
  let extText = $state(restored?.extText ?? '')
  let extQuery = $state(restored?.extQuery ?? '')
  let sortKey = $state<SortKey>(restored?.sortKey ?? 'relevance')

  /** `$state.raw`: the list is replaced wholesale on every flush, and a deep
   * proxy over a hundred thousand hits costs on every read. */
  let hits = $state.raw<readonly SearchHit[]>(restored?.hits ?? [])
  // An in-flight stream cannot resume after its component unmounts. Its
  // cleanup records that interruption, and a remount reports the retained
  // rows as partial instead of claiming a complete answer.
  let running = $state(false)
  /** A query has actually been submitted, so an empty list means "nothing
   *  matched" rather than "not asked yet". */
  let ran = $state(restored?.ran ?? false)
  let failure = $state<string | null>(restored?.running ? 'stopped' : (restored?.failure ?? null))
  let truncated = $state(restored?.truncated ?? false)
  let elapsedMs = $state<number | null>(restored?.elapsedMs ?? null)
  /** What the walk has looked through so far. A search of a large tree can
   *  run a long time before its first match, and a count that moves is the
   *  difference between "still going" and "stuck". */
  let scanned = $state<SearchProgress | null>(restored?.scanned ?? null)

  let cancel: (() => void) | null = null
  /** Hits arrive one frame at a time; rendering per hit would spend the
   *  search re-rendering, so they land here and go in as batches. */
  let arriving: SearchHit[] = []
  let flushTimer: ReturnType<typeof setTimeout> | null = null
  let runGeneration = 0

  const FLUSH_MS = 100
  const ROW_PX = 60

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
    runGeneration++
    const generation = runGeneration
    cancel?.()
    cancel = null
    if (flushTimer !== null) {
      clearTimeout(flushTimer)
      flushTimer = null
    }
    arriving = []
    hits = []
    failure = null
    truncated = false
    elapsedMs = null
    scanned = null
    scrollTop = 0
    restoredScrollTop = 0
    const text = query.trim()
    if (text === '') {
      ran = false
      running = false
      return
    }

    ran = true
    running = true
    const exts = resolveExtensions(presets, extQuery)
    const stopStream = api.searchStream(
      { query: text, kind: kind === 'any' ? undefined : kind, exts, scope: scope || undefined },
      (hit: SearchHit) => {
        if (generation !== runGeneration) return
        arriving.push(hit)
        scheduleFlush()
      },
      (done: SearchDone) => {
        if (generation !== runGeneration) return
        flush()
        running = false
        cancel = null
        elapsedMs = done.elapsedMs ?? null
        failure = done.error ?? null
        truncated = done.truncated === true
      },
      (p: SearchProgress) => {
        if (generation !== runGeneration) return
        scanned = p
      }
    )
    if (generation === runGeneration && running) cancel = stopStream
    else stopStream()
  }

  /** Stops the walk where it is. What arrived stays on screen and says it is
   *  partial, which is the honest reading of a search someone interrupted. */
  function stop(): void {
    if (!running) return
    runGeneration++
    cancel?.()
    cancel = null
    flush()
    running = false
    failure = 'stopped'
    truncated = false
  }

  /** Back to an empty search, filters included: a leftover extension is the
   *  quiet reason a later query finds nothing. */
  function clear(): void {
    runGeneration++
    cancel?.()
    cancel = null
    if (flushTimer !== null) {
      clearTimeout(flushTimer)
      flushTimer = null
    }
    arriving = []
    hits = []
    running = false
    restoredScrollTop = 0
    ran = false
    failure = null
    truncated = false
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
      // server refuses it. The extension controls are gone while folders are
      // the target rather than sitting there saying why they would not work.
      presets = []
      extText = ''
      extQuery = ''
    }
  }

  /** The typed extensions as last committed. A search here walks whole trees,
   *  so it runs when the box is left or Enter is pressed, not on the way to
   *  spelling "pdf". */
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
    const wasRunning = running
    runGeneration++
    cancel?.()
    cancel = null
    if (flushTimer !== null) {
      clearTimeout(flushTimer)
      flushTimer = null
    }
    flush()
    // A stream cannot continue after this component unmounts. Keep what
    // arrived and make the interruption explicit when the route remounts.
    persistSnapshot({ running: false, failure: wasRunning ? 'stopped' : failure })
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

  const fileCount = $derived(view.length - dirCount)

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

  // Escape belongs to an open menu. Without this the key reached the sheet's
  // own dialog underneath and closed the whole search.
  function onKeydownCapture(e: KeyboardEvent): void {
    if (e.key !== 'Escape') return
    if (filtersOpen) {
      e.preventDefault()
      e.stopPropagation()
      closeFilters()
      return
    }
    if (!sortOpen) return
    e.preventDefault()
    e.stopPropagation()
    closeSort()
  }

  function closeFilters(): void {
    filtersOpen = false
    filtersTrigger?.focus()
    filtersTrigger = null
  }

  /** What is narrowing the search, in the words the controls use.
   *
   *  Groups by their own name rather than by what they expand to: one tap on
   *  "images" is twelve extensions, and a line reading "jpg, jpeg, png, gif,
   *  webp, heic…" says less about the filter than its name does. */
  const activeFilters = $derived.by(() => {
    const parts: string[] = []
    for (const id of presets) {
      const preset = EXTENSION_PRESETS.find((p) => p.id === id)
      if (preset) parts.push(t(preset.labelKey))
    }
    parts.push(...parseExtensions(extQuery))
    return parts.join(', ')
  })

  const SORT_KEYS = [
    ['relevance', /* i18n */ 'search.sort_relevance'],
    ['name', /* i18n */ 'search.sort_name'],
    ['size', /* i18n */ 'search.sort_size'],
    ['date', /* i18n */ 'search.sort_date']
  ] as const

  const sortLabel = $derived(t(SORT_KEYS.find(([id]) => id === sortKey)?.[1] ?? 'search.sort_relevance'))

  let sortOpen = $state(false)
  let sortX = $state(0)
  let sortY = $state(0)
  let sortMenuEl: HTMLDivElement | undefined = $state()
  let sortTrigger: HTMLElement | null = null

  function openSort(e: MouseEvent): void {
    if (sortOpen) {
      closeSort()
      return
    }
    e.stopPropagation()
    const btn = e.currentTarget as HTMLElement
    sortTrigger = btn
    const rect = btn.getBoundingClientRect()
    sortX = rect.right
    sortY = rect.bottom + 4
    sortOpen = true
    queueMicrotask(() => sortMenuEl?.querySelector<HTMLButtonElement>('button')?.focus())
  }

  function closeSort(): void {
    sortOpen = false
    sortTrigger?.focus()
    sortTrigger = null
  }

  function chooseSort(next: SortKey): void {
    sortKey = next
    closeSort()
  }

  let fieldEl: HTMLDivElement | undefined = $state()
  let listEl: HTMLDivElement | undefined = $state()
  let scrollTop = $state(0)
  let restoredScrollTop = restored?.scrollTop ?? 0

  function snapshotOf(overrides: Partial<Pick<SearchSnapshot, 'running' | 'failure' | 'truncated'>> = {}): SearchSnapshot {
    return {
      scope,
      query,
      kind,
      presets,
      extText,
      extQuery,
      sortKey,
      hits,
      running,
      ran,
      failure,
      truncated,
      elapsedMs,
      scanned,
      scrollTop,
      ...overrides
    }
  }

  function persistSnapshot(overrides: Partial<Pick<SearchSnapshot, 'running' | 'failure' | 'truncated'>> = {}): void {
    search.saveSnapshot(snapshotOf(overrides))
  }

  // Bindings such as the query field and list scroll update local state
  // directly. One effect records the complete state after each such update,
  // so route changes cannot drop a refinement that has not been submitted yet.
  $effect(() => {
    persistSnapshot()
  })

  $effect(() => {
    const el = listEl
    const count = view.length
    const offset = restoredScrollTop
    if (!el || count === 0 || offset <= 0) return
    queueMicrotask(() => {
      if (!listEl || restoredScrollTop !== offset) return
      listEl.scrollTop = offset
      scrollTop = offset
      restoredScrollTop = 0
    })
  })
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
    // Save before closing the sheet or unmounting the mobile route. The
    // containing folder can then reopen the same result list and position.
    persistSnapshot()
    onnavigated?.()
    // Browse reveals the exact hit from its parent folder, rather than merely
    // opening a folder and making the user search for the selected item again.
    const target = folderOf(hit.path)
    void goto(`/b${target}?focus=${encodeURIComponent(hit.entry.name)}`)
  }

  const statusText = $derived.by(() => {
    if (running) {
      const found = t('search.searching', { count: hits.length })
      // The folder count only appears once the server has sent one, which on
      // a search that answers in a moment it never does.
      return scanned === null ? found : `${found}, ${t('search.scanning', { dirs: formatNumber(scanned.dirs) })}`
    }
    if (!ran) return ''
    // Server truncation is not a request failure: the rows are still useful,
    // but they must not be presented as the complete answer. User stop and
    // transport failure remain separate outcomes below.
    if (truncated) return t('search.incomplete', { count: view.length })
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

  <div class="sc-search__scope" aria-describedby="sc-search-scope-note">
    <span class="sc-search__scope-label">{t('search.scope_all_accessible')}</span>
    <span id="sc-search-scope-note">
      {#if scope}
        {t('search.scope_current_prioritized', { folder: scope })}
      {:else}
        {t('search.scope_explanation')}
      {/if}
    </span>
  </div>
  <div class="sc-search__filters">
    <!-- The kind is three buttons rather than a row in a menu: it is the
         filter people reach for, it has three values, and shown here it says
         what it is set to without anything being opened. -->
    <span class="sc-search__label" id="sc-search-kind">{t('search.kind_label')}</span>
    <ConnectedButtons role="group" aria-labelledby="sc-search-kind">
      {#each [['any', t('search.kind_any')], ['file', t('search.kind_file')], ['dir', t('search.kind_dir')]] as const as [id, label] (id)}
        <Button
          square
          variant={kind === id ? 'filled' : 'tonal'}
          pressed={kind === id}
          onclick={() => setKind(id as Kind)}
        >
          {label}
        </Button>
      {/each}
    </ConnectedButtons>

    <!-- Gone, not disabled, while folders are the target: a folder carries no
         extension, so every one of these would narrow the answer to nothing. -->
    {#if kind !== 'dir'}
      <Button variant="outlined" onclick={openFilters}>
        {#snippet icon()}<Icon icon={icons.filter} size={18} />{/snippet}
        {t('search.file_type')}
      </Button>
      <!-- The chosen groups, each one its own remove target. A summary line
           said the same thing and took a trip to the menu to undo. -->
      {#each presets as id (id)}
        {@const preset = EXTENSION_PRESETS.find((p) => p.id === id)}
        {#if preset}
          <Chip
            variant="filter"
            selected
            onremove={() => togglePreset(id)}
            ariaLabel={t('search.remove_filter', { label: t(preset.labelKey) })}
          >
            {t(preset.labelKey)}
          </Chip>
        {/if}
      {/each}

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
    {/if}
  </div>

  <Menu open={filtersOpen} onclose={closeFilters} x={filtersX} y={filtersY} align="start">
    <div bind:this={filtersMenuEl} role="none">
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
      <!-- The split of the same list sits beside the control that reorders
           it, away from the total on the left: two counts run together in
           one sentence read as one number nobody can parse. Only when the
           list holds both, since "0 folders" beside a total says nothing. -->
      {#if !running && fileCount > 0 && dirCount > 0}
        <span class="sc-search__breakdown">{t('search.summary', { files: fileCount, folders: dirCount })}</span>
      {/if}
      {#if running}
        <Button variant="text" onclick={stop}>{t('search.stop')}</Button>
      {/if}
      {#if ran && view.length > 0}
        <Button variant="text" onclick={openSort} ariaLabel={t('search.sort_by', { key: sortLabel })}>
          {#snippet icon()}<Icon icon={icons.sort} size={18} />{/snippet}
          {sortLabel}
        </Button>
      {/if}
    </span>
  </div>

  <Menu open={sortOpen} onclose={closeSort} x={sortX} y={sortY} align="end">
    <div bind:this={sortMenuEl} role="none">
      {#each SORT_KEYS as [id, labelKey] (id)}
        <MenuItem icon={sortKey === id ? icons.check : 'space'} onclick={() => chooseSort(id)}>
          {t(labelKey)}
        </MenuItem>
      {/each}
    </div>
  </Menu>

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
              <!-- Two lines: the name reads at full width and the folder sits
                   under it. Side by side, a long path took the width the name
                   needed and both ended in an ellipsis. -->
              <button class="sc-search__row" type="button" onclick={() => open(hit)}>
                <Icon icon={icons[hit.entry.kind === 'dir' ? 'folder' : 'file']} size={20} />
                <span class="sc-search__text">
                  <span class="sc-search__name">{hit.entry.name}</span>
                  <span class="sc-search__folder">{folderOf(hit.path)}</span>
                </span>
                <span class="sc-search__cell">
                  {#if hit.entry.kind !== 'dir'}
                    <span class="sc-search__size">{formatBytes(hit.entry.size)}</span>
                  {/if}
                  <span class="sc-search__date">{formatDateNs(hit.entry.mtime_ns)}</span>
                </span>
              </button>
            </li>
          {/each}
        </ul>
      </div>
    {/if}
  </div>
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
  .sc-search__scope {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: 4px 8px;
    color: var(--m3c-on-surface-variant, inherit);
    font: var(--m3-font-body-small);
  }

  .sc-search__scope-label {
    color: var(--m3c-on-surface, inherit);
    font: var(--m3-font-label-large);
  }

  .sc-search__filters {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px;
  }

  .sc-search__label {
    color: var(--m3c-on-surface-variant, inherit);
    font: var(--m3-font-label-large);
  }

  .sc-search__exts {
    /* Sized to the list it holds rather than the row it sits in: stretched
       across the remaining width it read as the main input. */
    flex: 0 1 200px;
    min-width: 140px;
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
    align-items: baseline;
    gap: 8px;
    min-width: 0;
    overflow: hidden;
    font: var(--m3-font-body-medium);
    color: var(--m3c-on-surface-variant, inherit);
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .sc-search__breakdown {
    font: var(--m3-font-body-small);
  }

  .sc-search__status-actions {
    display: flex;
    flex: 0 0 auto;
    align-items: center;
    gap: 8px;
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
    grid-template-columns: 20px minmax(0, 1fr) auto;
    align-items: center;
    gap: 12px;
    width: 100%;
    height: 60px;
    padding-inline: 12px;
    border: none;
    background: none;
    color: inherit;
    font: var(--m3-font-body-medium);
    text-align: start;
    cursor: pointer;
  }

  .sc-search__text,
  .sc-search__cell {
    display: flex;
    flex-direction: column;
    /* The row is one fixed height the virtual list measures every row by, so
       neither column may grow: both lines are single-line and clipped. */
    gap: 0;
    min-width: 0;
  }

  .sc-search__cell {
    align-items: end;
    flex: 0 0 auto;
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
    white-space: nowrap;
    overflow: hidden;
  }

  .sc-search__note {
    margin: 0;
    padding: 16px;
    color: var(--m3c-on-surface-variant, inherit);
    font: var(--m3-font-body-medium);
  }

  @media (max-width: 600px) {
    /* The date is the first thing to go: a phone row has room for the name,
       the folder under it and one number. */
    .sc-search__date,
    .sc-search__breakdown {
      display: none;
    }
  }
</style>
