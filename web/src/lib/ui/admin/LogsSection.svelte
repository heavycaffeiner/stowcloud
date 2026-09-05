<script lang="ts">
  // The whole log feature on one screen: what the server recorded about
  // itself (`GET /api/v1/admin/logs`) and what accounts did
  // (`GET /api/v1/admin/audit`), under one filter bar, with a graph over the
  // filtered window on top of both.
  //
  // The two resources stay separate on the server. They have different
  // retention, different volume and different meaning, and each keeps its own
  // cursor. The merge is a screen decision, made in `admin/log-view.ts`.
  //
  // Filters are client state (`logsForm`, in `store/logs.store.ts`): what the
  // operator has typed. `debounced` settles that into the value the three
  // queries key on, so a keystroke is not a request. A filter change becomes
  // a new query key rather than something this file has to cancel and
  // reconcile: the old key's answer, if it is still in flight, lands where
  // nothing observes it any more.
  //
  // Display decisions the contract forces:
  //
  // `ts_ns`, `stored_bytes`, `start_ns` and `bucket_ns` stay strings from the
  // wire to the formatter. All of them outgrow an exact JavaScript number.
  // `formatDateNs` takes the string and goes through BigInt; the stored size
  // is divided down in BigInt before it is ever a number, so the lossy step
  // happens where loss is the point.
  //
  // The graph reads the timeline endpoint, never the loaded page. A page is
  // the newest fifty of each stream; a chart drawn from it would be a chart
  // of how far the reader has scrolled rather than of the window they filtered.
  //
  // Both cursors are opaque or positional, so this is a "load more" and not a
  // page-number pager: the client cannot address page four, only ask for what
  // follows what it holds. One button advances both streams and appends.
  //
  // The subsystem filter is a text field with suggestions, not a `<select>`.
  // Only `dav` is instrumented today and more arrive one subsystem at a time,
  // so a closed list would refuse a value the server is already sending.
  import { createInfiniteQuery, createQuery } from '@tanstack/svelte-query'
  import { ALL_LOG_LEVELS, type AdminLogRecord, type AuditRow } from '../../api/client'
  import Button from '../Button.svelte'
  import Checkbox from '../Checkbox.svelte'
  import { ConnectedButtons, Icon } from 'm3-svelte'
  import { icons } from '../../icons'
  import ProgressCircular from '../ProgressCircular.svelte'
  import TextField from '../TextField.svelte'
  import { formatDateNs, formatDuration, formatNumber, t } from '../../i18n'
  import {
    DEBOUNCE_MS,
    MAX_RECORDS,
    pureActorLabel,
    pureIncludesAudit,
    pureIncludesServer,
    pureInterleave,
    pureKnownSubsystems,
    pureServerOnlyFiltersActive,
    pureTimelineView,
    type LogSourceMode,
    type TimelineBar,
    type TimelineSeries,
    type UnifiedLogItem
  } from '../../admin/log-view'
  import { adminUsersQuery } from '../../query/admin'
  import { debounced } from '../../query/debounce.svelte'
  import { adminAuditQuery, adminLogsQuery, adminTimelineQuery } from '../../query/logs'
  import { logsForm } from '../../store/logs.store'

  // The fields on screen track the live value; the three queries key on the
  // settled one, so typing updates the form at once and the network only
  // once the operator pauses.
  const filters = $derived(logsForm.state.filters)
  const settled = debounced(() => logsForm.state.filters, DEBOUNCE_MS)

  const includesServer = $derived(pureIncludesServer(settled.current.sourceMode))
  const includesAudit = $derived(pureIncludesAudit(settled.current.sourceMode))

  const logs = createInfiniteQuery(() => adminLogsQuery(settled.current, includesServer))
  const audit = createInfiniteQuery(() => adminAuditQuery(settled.current, includesAudit))
  const timeline = createQuery(() => adminTimelineQuery(settled.current, true))
  const users = createQuery(() => adminUsersQuery())

  // Gated on the mode, not only fetched under it. Switching a stream off
  // disables its query, and a disabled query keeps whatever it last held, so
  // reading `data` unconditionally left the rows of a stream the operator had
  // just switched off on screen.
  const records = $derived(includesServer ? (logs.data?.pages.flatMap((p) => p.records) ?? []) : [])
  const auditRows = $derived(includesAudit ? (audit.data?.pages.flatMap((p) => p.rows) ?? []) : [])
  const items = $derived(pureInterleave(records, auditRows, MAX_RECORDS))
  const view = $derived(pureTimelineView(timeline.data ?? null, settled.current.sourceMode))
  const serverOnlyActive = $derived(pureServerOnlyFiltersActive(filters))
  const knownSubsystems = $derived(pureKnownSubsystems(records))

  // A stream the mode has switched off is a disabled query: it never fetched,
  // so its own flags would say "pending" forever. Gated on the same mode the
  // query itself is enabled with.
  const loading = $derived((includesServer && logs.isPending) || (includesAudit && audit.isPending))
  const loadingMore = $derived(
    (includesServer && logs.isFetchingNextPage) || (includesAudit && audit.isFetchingNextPage)
  )
  const failed = $derived((includesServer && logs.isError) || (includesAudit && audit.isError))
  const hasMore = $derived((includesServer && logs.hasNextPage) || (includesAudit && audit.hasNextPage))
  const truncated = $derived(items.length >= MAX_RECORDS && hasMore)

  function loadMore(): void {
    if (includesServer && logs.hasNextPage && !logs.isFetchingNextPage) void logs.fetchNextPage()
    if (includesAudit && audit.hasNextPage && !audit.isFetchingNextPage) void audit.fetchNextPage()
  }

  /** Stored size, formatted without ever holding the byte count in a number.
   *  BigInt divides to whole mebibytes first; what reaches `formatNumber` is
   *  small enough to be exact. Below a mebibyte it says so rather than
   *  rounding to zero, which would read as "nothing is stored". The figure is
   *  server-wide, and every page repeats the value as of its own request, so
   *  the most recently answered page is the one to read it from. */
  const newestPage = $derived(logs.data?.pages.at(-1))
  const storedLabel = $derived.by(() => {
    const mib = BigInt(newestPage?.stored_bytes ?? '0') / 1_048_576n
    return mib === 0n
      ? t('logs.under_one_mb')
      : t('logs.megabytes', { size: formatNumber(Number(mib)) })
  })
  const segments = $derived(newestPage?.segments ?? 0)

  const LEVEL_KEY: Record<string, string> = {
    DEBUG: /* i18n */ 'logs.level_debug',
    INFO: /* i18n */ 'logs.level_info',
    WARN: /* i18n */ 'logs.level_warn',
    ERROR: /* i18n */ 'logs.level_error'
  }

  // Icon per level. The badge already carries the word, so this is the third
  // channel beside text and colour rather than the only one. A level this
  // build has not heard of falls through to the neutral one.
  const LEVEL_ICON: Record<string, keyof typeof icons> = {
    DEBUG: 'search',
    INFO: 'info',
    WARN: 'warning',
    ERROR: 'warning'
  }

  const OUTCOME_KEY: Record<string, string> = {
    ok: /* i18n */ 'audit.success',
    failed: /* i18n */ 'audit.failure'
  }

  /** A series name as a person reads it. Falls through to the bare wire name
   *  for a level or outcome this build has not heard of, which is the same
   *  choice `AdminLogRecord.level` is widened to `string` for. */
  function seriesLabel(s: TimelineSeries): string {
    const key = s.source === 'server' ? LEVEL_KEY[s.name] : OUTCOME_KEY[s.name]
    return key ? t(key) : s.name
  }

  const SOURCE_MODES: { mode: LogSourceMode; key: string }[] = [
    { mode: 'all', key: /* i18n */ 'logs.source_all' },
    { mode: 'server', key: /* i18n */ 'logs.server_log' },
    { mode: 'audit', key: /* i18n */ 'common.audit_log' }
  ]

  // ── the graph's roving tab stop ──
  //
  // One tab stop for the whole plot, arrows within it. Forty-eight buckets as
  // forty-eight tab stops is a plot nobody tabs past, which is the standard
  // reason a grouped set of controls gets a roving index rather than a stop
  // each.
  //
  // The newest bucket is the default, not the oldest: it is the one an
  // operator opened the screen for. Buckets arrive oldest first, so that is
  // the last one.
  const barCount = $derived(view?.bars.length ?? 0)
  const activeBucket = $derived(
    barCount === 0 ? -1 : Math.min(logsForm.state.focusedBucket ?? barCount - 1, barCount - 1)
  )
  const activeBar = $derived(activeBucket < 0 ? null : (view?.bars[activeBucket] ?? null))

  let barEls: HTMLButtonElement[] = $state([])

  function moveFocus(to: number): void {
    if (barCount === 0) return
    const next = Math.max(0, Math.min(to, barCount - 1))
    logsForm.focusBucket(next)
    // The element has to be told to take focus: the roving index moves the
    // tabbability, and without this the reader's focus stays on the bar they
    // arrowed away from.
    barEls[next]?.focus()
  }

  function onPlotKeydown(e: KeyboardEvent, index: number): void {
    const step =
      e.key === 'ArrowRight'
        ? 1
        : e.key === 'ArrowLeft'
          ? -1
          : e.key === 'Home'
            ? -barCount
            : e.key === 'End'
              ? barCount
              : 0
    if (step === 0) return
    e.preventDefault()
    moveFocus(index + step)
  }

  /** The bucket's span, its total and every series count, as one sentence.
   *  This is the bar's accessible name: the numbers a sighted reader gets
   *  from the stack's heights have to be reachable as text, and a bar
   *  labelled only with its time is a bar whose numbers are colour alone. */
  function barName(bar: TimelineBar): string {
    const range = t('logs.bucket_range', {
      start: formatDateNs(bar.startNs),
      end: formatDateNs(bar.endNs)
    })
    const total = t('logs.event_count', { count: formatNumber(bar.total) })
    if (bar.segments.length === 0) return `${range}. ${total}`
    const parts = bar.segments.map((s) =>
      t('logs.series_count', {
        name: seriesLabel(s),
        count: formatNumber(s.count)
      })
    )
    return `${range}. ${total}. ${parts.join(', ')}`
  }

  /** Each bar's span as a duration a person would say out loud. Nanoseconds
   *  divide to seconds in BigInt first; a sub-second bucket reports as zero
   *  seconds rather than as a rounded fraction, and the plot says so. */
  const bucketWidthLabel = $derived.by(() => {
    if (view === null) return ''
    const seconds = Number(BigInt(view.bucketNs) / 1_000_000_000n)
    return t('logs.bucket_width', { duration: formatDuration(Math.max(seconds, 1)) })
  })

  function countIn(bar: TimelineBar, key: string): number {
    return bar.segments.find((s) => s.key === key)?.count ?? 0
  }

  // ── the list ──

  /** The account an audit row is attributed to. Best effort: a failure to
   *  load the user list costs a name, not the row. */
  function actorText(row: AuditRow): string {
    const label = pureActorLabel(row, users.data ?? [])
    if (label.kind === 'system') return t('common.system')
    if (label.kind === 'name') return label.name
    return t('common.user', { id: label.id })
  }

  /** The audit row's supporting fields, as a list the meta row can separate
   *  with its own CSS rather than with punctuation a screen reader speaks. */
  function auditMeta(row: AuditRow): string[] {
    const out = [formatDateNs(row.ts_ns), actorText(row)]
    if (row.target) out.push(row.target)
    if (row.ip) out.push(row.ip)
    if (row.detail) out.push(row.detail)
    return out
  }

  function itemKey(item: UnifiedLogItem): string {
    return item.key
  }
</script>

<section class="sc-logs">
  <h2>{t('common.logs')}</h2>
  <p class="sc-logs__hint">{t('logs.what_the_logs_are')}</p>

  <dl class="sc-logs__figures">
    <div>
      <dt>{t('logs.stored_size')}</dt>
      <dd>{storedLabel}</dd>
    </div>
    <div>
      <dt>{t('logs.segments')}</dt>
      <dd>{formatNumber(segments)}</dd>
    </div>
  </dl>

  <!-- `onsubmit` only to stop Enter from reloading the page: every field
       already writes straight to `logsForm`, and the debounced value the
       queries key on settles a moment later on its own. -->
  <form class="sc-logs__filters" onsubmit={(e) => e.preventDefault()}>
    <div class="sc-logs__sources">
      <span class="sc-logs__group-label" id="sc-logs-source-label">{t('logs.source')}</span>
      <!-- Three states, not two checkboxes. Two checkboxes have a both-off
           state that shows an empty screen and means nothing, and a pair that
           refuses to let you clear the last one is a control that lies about
           being a checkbox. -->
      <ConnectedButtons role="group" aria-labelledby="sc-logs-source-label">
        {#each SOURCE_MODES as option (option.mode)}
          <Button
            square
            variant={filters.sourceMode === option.mode ? 'filled' : 'tonal'}
            pressed={filters.sourceMode === option.mode}
            onclick={() => logsForm.patch({ sourceMode: option.mode })}
          >
            {t(option.key)}
          </Button>
        {/each}
      </ConnectedButtons>
    </div>

    <!-- A real fieldset, so the four boxes are announced as one group called
         "Level" rather than as four unrelated checkboxes. -->
    <fieldset class="sc-logs__levels">
      <legend>{t('logs.level')}</legend>
      <div class="sc-logs__level-boxes">
        {#each ALL_LOG_LEVELS as level (level)}
          <Checkbox
            checked={filters.levels.has(level)}
            label={t(LEVEL_KEY[level])}
            onchange={() => logsForm.toggleLevel(level)}
          />
        {/each}
      </div>
    </fieldset>

    <div class="sc-logs__fields">
      <TextField
        label={t('logs.search_text')}
        placeholder={t('logs.e_g_refused')}
        type="search"
        value={filters.text}
        oninput={(v) => logsForm.patch({ text: v })}
        autocomplete="off"
      />
      <TextField
        label={t('logs.subsystem')}
        placeholder={t('logs.e_g_dav')}
        list="sc-logs-subsystems"
        value={filters.subsystem}
        oninput={(v) => logsForm.patch({ subsystem: v })}
        autocomplete="off"
      />
      <!-- Suggestions, not a closed set: any subsystem string is accepted. -->
      <datalist id="sc-logs-subsystems">
        {#each knownSubsystems as s (s)}<option value={s}></option>{/each}
      </datalist>
      <TextField
        label={t('logs.request_id')}
        placeholder={t('logs.e_g_request_id')}
        value={filters.requestId}
        oninput={(v) => logsForm.patch({ requestId: v })}
        autocomplete="off"
      />
      <TextField
        label={t('logs.from')}
        type="datetime-local"
        value={filters.since}
        oninput={(v) => logsForm.patch({ since: v })}
      />
      <TextField
        label={t('logs.to')}
        type="datetime-local"
        value={filters.until}
        oninput={(v) => logsForm.patch({ until: v })}
      />
    </div>

    <!-- Said out loud rather than left to be inferred. Level, text, subsystem
         and request id narrow the server log; an audit row carries none of
         those fields, so the server does not filter the audit half by them
         and neither does this screen. Silently dropping audit rows whenever a
         level is picked would hide rows the query never excluded. -->
    {#if serverOnlyActive && filters.sourceMode !== 'server'}
      <p class="sc-logs__scope" role="status">
        <Icon icon={icons.info} size={16} />
        <span>{t('logs.server_only_filters_note')}</span>
      </p>
    {/if}

    <Button variant="tonal" type="submit">{t('logs.apply_filters')}</Button>
  </form>

  <!-- ── the graph ──
       Counts come from the timeline endpoint, so they are exact over the
       whole filtered window rather than over the page the list happens to
       hold. It fails on its own: a server that cannot answer it still serves
       the list below, so a lost chart is a note, not an empty screen. -->
  <section class="sc-logs__chart" aria-labelledby="sc-logs-chart-title">
    <div class="sc-logs__chart-head">
      <h3 id="sc-logs-chart-title">{t('logs.timeline')}</h3>
      <p class="sc-logs__hint">{t('logs.timeline_description')}</p>
    </div>

    {#if timeline.isPending && view === null}
      <ProgressCircular label={t('logs.loading_timeline')} />
    {:else if timeline.isError && view === null}
      <p class="sc-logs__note" role="status">{t('logs.could_not_load_timeline')}</p>
    {:else if view !== null}
      {#if view.truncated}
        <!-- `truncated` on screen, not only in the payload: a partial graph
             that looks whole is a graph that misleads. -->
        <p class="sc-logs__warn" role="status">
          <Icon icon={icons.warning} size={16} />
          <span>{t('logs.timeline_truncated')}</span>
        </p>
      {/if}

      {#if view.total === 0}
        <div class="sc-logs__empty">
          <Icon icon={icons.list} size={28} />
          <p>{t('logs.no_events_in_window')}</p>
          <p class="sc-logs__empty-hint">{t('logs.widen_the_filters')}</p>
        </div>
      {:else}
        <!-- The legend. Never colour alone: each series carries its name as
             text and its swatch carries the same pattern the stack uses, so
             the three channels are word, texture and hue. -->
        <ul class="sc-logs__legend">
          {#each view.series as s (s.key)}
            <li>
              <span class="sc-logs__swatch sc-logs__seg--{s.source}-{s.name.toLowerCase()}"></span>
              <span class="sc-logs__legend-source">
                {s.source === 'server' ? t('logs.server_log') : t('common.audit_log')}
              </span>
              <span>{seriesLabel(s)}</span>
            </li>
          {/each}
        </ul>

        <p class="sc-sr-only" id="sc-logs-plot-hint">
          {t('logs.arrow_keys_move_between_buckets')}
        </p>

        <div
          class="sc-logs__plot"
          role="group"
          aria-labelledby="sc-logs-chart-title"
          aria-describedby="sc-logs-plot-hint"
        >
          {#each view.bars as bar, i (bar.startNs)}
            <!-- One tab stop for the plot, arrows within it. The bar is a
                 real `<button>`, so its accessible name (the bucket's span
                 and every count in it) is what a reader hears on arrival,
                 without a mouse and without reading a colour. -->
            <button
              type="button"
              class="sc-logs__bar sc-focus-ring"
              class:sc-logs__bar--active={i === activeBucket}
              tabindex={i === activeBucket ? 0 : -1}
              aria-label={barName(bar)}
              aria-current={i === activeBucket ? 'true' : undefined}
              bind:this={barEls[i]}
              onclick={() => logsForm.focusBucket(i)}
              onfocus={() => logsForm.focusBucket(i)}
              onkeydown={(e) => onPlotKeydown(e, i)}
            >
              <span class="sc-logs__stack">
                {#if bar.total === 0}
                  <!-- A bucket with nothing in it is still a bucket. The
                       baseline says so; a gap would read as missing data. -->
                  <span class="sc-logs__baseline"></span>
                {:else}
                  {#each bar.segments as seg (seg.key)}
                    <span
                      class="sc-logs__seg sc-logs__seg--{seg.source}-{seg.name.toLowerCase()}"
                      style="height: {seg.percent}%"
                    ></span>
                  {/each}
                {/if}
              </span>
            </button>
          {/each}
        </div>

        <div class="sc-logs__axis">
          <span>{formatDateNs(view.bars[0].startNs)}</span>
          <span>{bucketWidthLabel}</span>
          <span>{formatDateNs(view.bars[view.bars.length - 1].endNs)}</span>
        </div>

        <!-- The focused bucket's numbers as text, beside the plot rather than
             only inside an accessible name: a sighted keyboard user arrowing
             across the bars gets the same readout the screen reader does. -->
        {#if activeBar !== null}
          <p class="sc-logs__readout" role="status" aria-live="polite">{barName(activeBar)}</p>
        {/if}

        <!-- The data-table equivalent. Behind a disclosure rather than
             visually hidden, so it is reachable by everyone and not only by
             assistive technology. -->
        <details class="sc-logs__table-wrap">
          <summary class="sc-focus-ring">{t('logs.show_the_numbers')}</summary>
          <div class="sc-logs__table-scroll">
            <table class="sc-logs__table">
              <caption>{t('logs.timeline_table_caption')}</caption>
              <thead>
                <tr>
                  <th scope="col">{t('logs.time')}</th>
                  {#each view.series as s (s.key)}
                    <th scope="col">
                      {s.source === 'server' ? t('logs.server_log') : t('common.audit_log')}
                      {seriesLabel(s)}
                    </th>
                  {/each}
                  <th scope="col">{t('logs.total')}</th>
                </tr>
              </thead>
              <tbody>
                {#each view.bars as bar (bar.startNs)}
                  <tr>
                    <th scope="row">{formatDateNs(bar.startNs)}</th>
                    {#each view.series as s (s.key)}
                      <td>{formatNumber(countIn(bar, s.key))}</td>
                    {/each}
                    <td>{formatNumber(bar.total)}</td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        </details>
      {/if}
    {/if}
  </section>

  <!-- ── the list ──
       Both streams interleaved newest first, each row saying which one it
       came from. -->

  <!-- The level, the message and the record's own metadata: the same in both
       row branches, so it lives in one place. -->
  {#snippet serverContent(r: AdminLogRecord)}
    <span class="sc-logs__level sc-logs__level--{r.level.toLowerCase()}">
      <Icon icon={icons[LEVEL_ICON[r.level] ?? 'info']} size={16} />
      {LEVEL_KEY[r.level] ? t(LEVEL_KEY[r.level]) : r.level}
    </span>
    <span class="sc-logs__body">
      <span class="sc-logs__msg">{r.msg}</span>
      <span class="sc-logs__meta">
        <span class="sc-logs__source">{t('logs.server_log')}</span>
        <span>{formatDateNs(r.ts_ns)}</span>
        {#if r.subsystem}<span>{r.subsystem}</span>{/if}
        {#if r.request_id}<span>{t('logs.request', { id: r.request_id })}</span>{/if}
      </span>
    </span>
  {/snippet}

  {#snippet auditContent(row: AuditRow)}
    <span class="sc-logs__level sc-logs__level--{row.ok ? 'ok' : 'failed'}">
      <Icon icon={icons[row.ok ? 'check' : 'warning']} size={16} />
      {row.ok ? t('audit.success') : t('audit.failure')}
    </span>
    <span class="sc-logs__body">
      <span class="sc-logs__msg">{row.event}</span>
      <span class="sc-logs__meta">
        <span class="sc-logs__source">{t('common.audit_log')}</span>
        {#each auditMeta(row) as part, i (i)}<span>{part}</span>{/each}
      </span>
    </span>
  {/snippet}

  <!-- Loading and empty are announced, not only drawn. The region is always
       in the tree and only its text changes: a live region inserted with its
       message already inside is frequently not spoken, since there was no
       change for the reader to observe. Polite, because none of this
       interrupts what the reader is doing. -->
  <p class="sc-sr-only" role="status" aria-live="polite">
    {#if loading}
      {t('logs.loading_logs')}
    {:else if failed}
      {t('logs.could_not_load_logs')}
    {:else if items.length === 0}
      {t('logs.no_records_match')}
    {:else}
      {t('logs.showing_records', { count: items.length })}
    {/if}
  </p>

  {#if loading}
    <ProgressCircular label={t('logs.loading_logs')} />
  {:else if failed}
    <p class="sc-logs__error" role="alert">{t('logs.could_not_load_logs')}</p>
  {:else if items.length === 0}
    <div class="sc-logs__empty">
      <Icon icon={icons.list} size={28} />
      <p>{t('logs.no_records_match')}</p>
      <p class="sc-logs__empty-hint">{t('logs.widen_the_filters')}</p>
    </div>
  {:else}
    <ul class="sc-logs__list">
      {#each items as item (itemKey(item))}
        <li class="sc-logs__item">
          {#if item.source === 'audit'}
            <!-- An audit row carries no attribute bag, so it has nothing to
                 disclose and is not a control. -->
            <div class="sc-logs__row">{@render auditContent(item.row)}</div>
          {:else}
            {@const attrs = Object.entries(item.record.attrs)}
            {@const open = logsForm.state.expanded.has(item.key)}
            <!-- The whole row is the disclosure control, so a record is one
                 tab stop and Enter/Space opens it: a real `<button>`, not a
                 div with a click handler and a role bolted on. A record with
                 no attributes has nothing to disclose, so it is a plain
                 `<div>` rather than a focusable control that does nothing.

                 Two spelled-out branches rather than one `<svelte:element>`
                 switching the tag: a dynamic tag defeats the compiler's own
                 a11y analysis, so the version that read as clever was the one
                 it could not check. The shared innards are a snippet, so
                 there is still one copy of them. -->
            {#if attrs.length > 0}
              <button
                type="button"
                class="sc-logs__row sc-logs__row--button m3-layer sc-focus-ring"
                aria-expanded={open}
                aria-controls={`sc-log-attrs-${item.key}`}
                onclick={() => logsForm.toggleExpanded(item.key)}
              >
                {@render serverContent(item.record)}
                <span class="sc-logs__disclose">
                  {t('logs.attribute_count', { count: attrs.length })}
                  <span class="sc-logs__chevron" class:sc-logs__chevron--open={open}>
                    <Icon icon={icons['chevron-right']} size={18} />
                  </span>
                </span>
              </button>
            {:else}
              <div class="sc-logs__row">{@render serverContent(item.record)}</div>
            {/if}

            {#if open}
              <dl class="sc-logs__attrs" id={`sc-log-attrs-${item.key}`}>
                {#each attrs as [k, v] (k)}
                  <div>
                    <dt>{k}</dt>
                    <dd>{v}</dd>
                  </div>
                {/each}
              </dl>
            {/if}
          {/if}
        </li>
      {/each}
    </ul>

    {#if truncated}
      <p class="sc-logs__note" role="status">
        {t('logs.reached_the_cap', { count: formatNumber(items.length) })}
      </p>
    {:else if hasMore}
      <div class="sc-logs__more">
        <Button variant="text" onclick={loadMore} loading={loadingMore}>
          {t('logs.load_more')}
        </Button>
      </div>
    {/if}
  {/if}
</section>

<style>
  .sc-logs {
    container-name: sc-logs;
    container-type: inline-size;
  }
  .sc-logs h2 {
    margin: 0 0 8px;
    @apply --m3-title-large;
  }
  .sc-logs h3 {
    margin: 0;
    @apply --m3-title-medium;
  }
  .sc-logs__hint {
    max-width: 640px;
    margin: 0 0 16px;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
  /* The same figure block as the index estimate on the storage section: two
     numbers side by side where there is room, stacked where there is not. */
  .sc-logs__figures {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(140px, 1fr));
    gap: 16px;
    max-width: 480px;
    margin: 0 0 24px;
  }
  .sc-logs__figures div {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .sc-logs__figures dt {
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
  .sc-logs__figures dd {
    margin: 0;
    color: var(--m3c-on-surface);
    @apply --m3-title-medium;
  }

  .sc-logs__filters {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: 16px;
    margin-bottom: 24px;
  }
  .sc-logs__sources {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  /* The same size and colour a fieldset's legend gets, so the source group
     and the level group read as siblings rather than as two different kinds
     of thing. */
  .sc-logs__group-label {
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
  /* The UA gives a fieldset its own border, padding and margin; this one is a
     grouping for assistive technology, not a drawn box. */
  .sc-logs__levels {
    margin: 0;
    padding: 0;
    border: 0;
  }
  .sc-logs__levels legend {
    padding: 0;
    margin-bottom: 8px;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
  .sc-logs__level-boxes {
    display: flex;
    flex-wrap: wrap;
    gap: 16px;
  }
  /* `TextField` sizes its box off its own wrapper, so in a row each field has
     to be told what share it takes, and the set is capped so a filter form
     does not stretch across a 1440px pane. */
  .sc-logs__fields {
    display: flex;
    flex-wrap: wrap;
    align-items: flex-end;
    gap: 16px;
    width: 100%;
  }
  @container sc-logs (min-width: 600px) {
    .sc-logs__fields > :global(.field) {
      flex: 1 1 0;
      min-width: 180px;
      max-width: 280px;
    }
  }
  @container sc-logs (max-width: 599.98px) {
    .sc-logs__fields {
      flex-direction: column;
      align-items: stretch;
    }
  }
  .sc-logs__scope,
  .sc-logs__warn {
    display: flex;
    align-items: flex-start;
    gap: 8px;
    max-width: 640px;
    margin: 0;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
  .sc-logs__warn {
    color: var(--m3c-on-tertiary-container);
    background: var(--m3c-tertiary-container);
    padding: 8px 12px;
    border-radius: var(--m3-shape-extra-small);
  }

  /* ── the graph ── */
  .sc-logs__chart {
    margin-bottom: 24px;
  }
  .sc-logs__chart-head {
    margin-bottom: 16px;
  }
  .sc-logs__chart-head .sc-logs__hint {
    margin: 4px 0 0;
  }

  .sc-logs__legend {
    display: flex;
    flex-wrap: wrap;
    gap: 8px 16px;
    margin: 0 0 12px;
    padding: 0;
    list-style: none;
  }
  .sc-logs__legend li {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    color: var(--m3c-on-surface);
    @apply --m3-label-medium;
  }
  .sc-logs__legend-source {
    color: var(--m3c-on-surface-variant);
  }
  .sc-logs__swatch {
    display: inline-block;
    width: 16px;
    height: 16px;
    border: 1px solid var(--m3c-outline);
    border-radius: var(--m3-shape-extra-small);
  }

  .sc-logs__plot {
    display: flex;
    align-items: flex-end;
    gap: 4px;
    height: 160px;
    padding: 8px;
    border: 1px solid var(--m3c-outline-variant);
    border-radius: var(--m3-shape-medium);
    background: var(--m3c-surface-container-lowest);
  }
  /* A `<button>` brings the UA's own background, border and font; the bar is
     the control, so it keeps the plot's box. */
  .sc-logs__bar {
    display: flex;
    align-items: flex-end;
    flex: 1 1 0;
    min-width: 4px;
    height: 100%;
    padding: 0;
    background: none;
    border: 0;
    border-radius: 2px;
    cursor: pointer;
  }
  /* The focused bucket is not marked by colour alone: it also keeps the
     browser's focus ring, and the readout below names it in words. */
  .sc-logs__bar--active {
    background: var(--m3c-surface-container-high);
    outline: 1px solid var(--m3c-outline);
  }
  .sc-logs__stack {
    display: flex;
    flex-direction: column-reverse;
    justify-content: flex-start;
    width: 100%;
    height: 100%;
  }
  .sc-logs__seg {
    width: 100%;
    min-height: 1px;
  }
  .sc-logs__baseline {
    width: 100%;
    height: 2px;
    background: var(--m3c-outline-variant);
  }

  /* Every series carries a texture as well as a hue, so the stack survives
     a monochrome print, a colour-vision deficiency and a dark theme. The
     pairings are container roles with their matching `on-` roles, which is
     what holds the contrast in both themes without hand-tuning either. */
  .sc-logs__seg--server-debug {
    background: var(--m3c-surface-container-highest);
  }
  .sc-logs__seg--server-info {
    background: var(--m3c-secondary-container);
    background-image: repeating-linear-gradient(
      45deg,
      transparent 0 3px,
      var(--m3c-on-secondary-container) 3px 4px
    );
  }
  .sc-logs__seg--server-warn {
    background: var(--m3c-tertiary-container);
    background-image: repeating-linear-gradient(
      -45deg,
      transparent 0 3px,
      var(--m3c-on-tertiary-container) 3px 4px
    );
  }
  .sc-logs__seg--server-error {
    background: var(--m3c-error-container);
    background-image: repeating-linear-gradient(
      90deg,
      transparent 0 2px,
      var(--m3c-on-error-container) 2px 4px
    );
  }
  .sc-logs__seg--audit-ok {
    background: var(--m3c-primary-container);
    background-image: repeating-linear-gradient(
      0deg,
      transparent 0 3px,
      var(--m3c-on-primary-container) 3px 4px
    );
  }
  .sc-logs__seg--audit-failed {
    background: var(--m3c-error-container-subtle);
    background-image:
      repeating-linear-gradient(45deg, transparent 0 3px, var(--m3c-on-error-container) 3px 4px),
      repeating-linear-gradient(-45deg, transparent 0 3px, var(--m3c-on-error-container) 3px 4px);
  }

  .sc-logs__axis {
    display: flex;
    justify-content: space-between;
    gap: 8px;
    margin-top: 8px;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
  .sc-logs__readout {
    margin: 8px 0 0;
    color: var(--m3c-on-surface);
    @apply --m3-body-small;
  }

  .sc-logs__table-wrap {
    margin-top: 12px;
  }
  /* Block rather than inline-block: an inline box leaves a line-box gap of
     about a pixel above it, which reads as an uneven inset on a container
     that has none. `fit-content` keeps the click target the width of the
     text rather than the whole row. */
  .sc-logs__table-wrap summary {
    display: block;
    width: fit-content;
    padding: 4px 0;
    color: var(--m3c-primary);
    cursor: pointer;
    @apply --m3-body-small;
  }
  /* A window can hold hundreds of buckets, so the table scrolls inside the
     page rather than pushing the list off the bottom of it. */
  .sc-logs__table-scroll {
    max-height: 320px;
    overflow: auto;
    margin-top: 8px;
    border: 1px solid var(--m3c-outline-variant);
    border-radius: var(--m3-shape-extra-small);
  }
  .sc-logs__table {
    border-collapse: collapse;
    width: 100%;
    @apply --m3-body-small;
  }
  .sc-logs__table caption {
    padding: 8px;
    text-align: start;
    color: var(--m3c-on-surface-variant);
  }
  .sc-logs__table th,
  .sc-logs__table td {
    padding: 4px 8px;
    text-align: start;
    white-space: nowrap;
    border-top: 1px solid var(--m3c-outline-variant);
  }
  .sc-logs__table thead th {
    position: sticky;
    top: 0;
    background: var(--m3c-surface-container-low);
  }
  .sc-logs__table td {
    text-align: end;
  }

  /* ── the list ── */
  .sc-logs__error {
    margin: 8px 0 0;
    color: var(--m3c-error);
    @apply --m3-body-small;
  }
  .sc-logs__note {
    margin: 16px 0 0;
    text-align: center;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
  .sc-logs__empty {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 8px;
    padding: 32px 16px;
    color: var(--m3c-on-surface-variant);
    text-align: center;
    border: 1px dashed var(--m3c-outline-variant);
    border-radius: var(--m3-shape-medium);
  }
  .sc-logs__empty p {
    margin: 0;
    @apply --m3-body-medium;
  }
  .sc-logs__empty-hint {
    @apply --m3-body-small;
  }

  .sc-logs__list {
    list-style: none;
    margin: 0;
    padding: 0;
    border: 1px solid var(--m3c-outline-variant);
    border-radius: var(--m3-shape-medium);
    overflow: hidden;
  }
  .sc-logs__item + .sc-logs__item {
    border-top: 1px solid var(--m3c-outline-variant);
  }
  .sc-logs__row {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 16px;
    width: 100%;
    min-height: var(--sc-row-height);
    padding-block: 8px;
    padding-inline: 16px;
    color: var(--m3c-on-surface);
    text-align: start;
  }
  .sc-logs__row--button {
    background: none;
    border: 0;
    font: inherit;
    cursor: pointer;
  }

  /* The level is never colour alone: the badge carries its own word and an
     icon, and colour is the third channel. */
  .sc-logs__level {
    display: inline-flex;
    align-items: center;
    flex-shrink: 0;
    gap: 4px;
    min-width: 88px;
    padding-block: 4px;
    padding-inline: 8px;
    border-radius: var(--m3-shape-extra-small);
    background: var(--m3c-surface-container-highest);
    color: var(--m3c-on-surface-variant);
    @apply --m3-label-medium;
  }
  .sc-logs__level--error,
  .sc-logs__level--failed {
    background: var(--m3c-error-container);
    color: var(--m3c-on-error-container);
  }
  .sc-logs__level--warn {
    background: var(--m3c-tertiary-container);
    color: var(--m3c-on-tertiary-container);
  }
  .sc-logs__level--info {
    background: var(--m3c-secondary-container);
    color: var(--m3c-on-secondary-container);
  }
  .sc-logs__level--ok {
    background: var(--m3c-primary-container);
    color: var(--m3c-on-primary-container);
  }

  .sc-logs__body {
    display: flex;
    flex-direction: column;
    gap: 4px;
    min-width: 0;
    flex: 1 0 12rem;
  }
  .sc-logs__msg {
    @apply --m3-body-medium;
  }
  .sc-logs__meta {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
  /* Which of the two logs the row came from, as a word rather than a colour
     or a position. The two streams obey different filters, so a reader has to
     be able to tell them apart at a glance. */
  .sc-logs__source {
    padding-inline: 4px;
    border: 1px solid var(--m3c-outline-variant);
    border-radius: var(--m3-shape-extra-small);
    color: var(--m3c-on-surface-variant);
  }
  /* A separator drawn by CSS rather than typed into the markup, so it is not
     read out as punctuation between every pair of fields. The source chip
     carries its own border, so the dot starts after it. */
  .sc-logs__meta > span + span:not(:nth-child(2))::before {
    content: '';
    display: inline-block;
    width: 4px;
    height: 4px;
    margin-inline-end: 8px;
    border-radius: 4px;
    vertical-align: middle;
    background: var(--m3c-outline-variant);
  }
  .sc-logs__disclose {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    flex-shrink: 0;
    margin-left: auto;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
  /* One chevron rotated rather than two icons swapped: the quarter turn is
     the affordance people read as "this opens downward", and it animates,
     which a swap cannot. Suppressed under reduced motion. */
  .sc-logs__chevron {
    display: inline-flex;
    transition: rotate 150ms var(--m3-easing-standard, ease-out);
  }
  .sc-logs__chevron--open {
    rotate: 90deg;
  }
  @media (prefers-reduced-motion: reduce) {
    .sc-logs__chevron {
      transition: none;
    }
  }

  .sc-logs__attrs {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
    gap: 8px;
    margin: 0;
    padding-block: 16px;
    padding-inline: 16px;
    background: var(--m3c-surface-container-low);
  }
  .sc-logs__attrs div {
    display: flex;
    flex-direction: column;
    gap: 4px;
    min-width: 0;
  }
  .sc-logs__attrs dt {
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
  .sc-logs__attrs dd {
    margin: 0;
    /* An attribute value is a path, an errno or a byte count, and it is worth
       nothing silently truncated. */
    overflow-wrap: anywhere;
    @apply --m3-body-small;
  }

  .sc-logs__more {
    display: flex;
    justify-content: center;
    margin-top: 16px;
  }
</style>
