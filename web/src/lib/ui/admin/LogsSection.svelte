<script lang="ts">
  // The server's own log, read back through
  // `GET /api/v1/admin/logs` (`go/engine/service/logbook`). Read-only: this
  // screen filters and pages a store the server writes, and never edits it.
  //
  // No logic lives here. Filters, page accumulation and request bookkeeping
  // are `store/slices/logs.slice.ts`; this file subscribes through the runes
  // bridge with a selector and dispatches actions. The slice is what holds
  // the debounce, the AbortController and the generation counter that drops a
  // response arriving for a filter the operator has already moved past.
  //
  // Three display decisions the contract forces:
  //
  // `ts_ns` and `stored_bytes` stay strings from the wire to the formatter.
  // Both outgrow an exact JavaScript number. `formatDateNs` takes the string
  // and goes through BigInt; the stored size is divided down in BigInt before
  // it is ever a number, so the lossy step happens where loss is the point.
  //
  // The cursor is opaque, so this is a "load more" and not a page-number
  // pager: the client cannot address page four, only ask for what follows
  // what it holds. The button goes when the cursor comes back empty.
  //
  // The subsystem filter is a text field with suggestions, not a `<select>`.
  // Only `dav` is instrumented today and more arrive one subsystem at a time,
  // so a closed list would refuse a value the server is already sending.
  import { ALL_LOG_LEVELS, type AdminLogRecord } from '../../api/client'
  import Button from '../Button.svelte'
  import Checkbox from '../Checkbox.svelte'
  import { Icon } from 'm3-svelte'
  import { icons } from '../../icons'
  import ProgressCircular from '../ProgressCircular.svelte'
  import TextField from '../TextField.svelte'
  import { formatDateNs, formatNumber, t } from '../../i18n'
  import { useRunesStore } from '../../store/core/bridge.svelte'
  import {
    createLogsStore,
    pureKnownSubsystems,
    pureRecordKey,
    type LogsAction
  } from '../../store/slices/logs.slice'

  const store = createLogsStore()

  // One selector over the whole snapshot: every field below is read on each
  // render anyway, so a narrower one would only add comparisons. What keeps
  // this cheap is that the reducer returns the same arrays and sets when
  // nothing changed, so an unrelated update is reference-equal.
  const handle = useRunesStore(store)
  const snap = $derived(handle.current)

  const filters = $derived(snap.filters)
  const records = $derived(snap.records)
  const knownSubsystems = $derived(pureKnownSubsystems(records))

  /** Stored size, formatted without ever holding the byte count in a number.
   *  BigInt divides to whole mebibytes first; what reaches `formatNumber` is
   *  small enough to be exact. Below a mebibyte it says so rather than
   *  rounding to zero, which would read as "nothing is stored". */
  const storedLabel = $derived.by(() => {
    const mib = BigInt(snap.storedBytes) / 1_048_576n
    return mib === 0n
      ? t('logs.under_one_mb')
      : t('logs.megabytes', { size: formatNumber(Number(mib)) })
  })

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

  /** Filters refetch after the slice's settling window, so a keystroke is not
   *  a request and the request in flight for the previous value is aborted. */
  function changeFilter(action: LogsAction): void {
    store.changeFilter(action)
  }

  store.refresh()

  // A dashboard left behind stops costing the server: the timer is cancelled
  // and the request in flight aborted when this section goes away.
  $effect(() => () => store.dispose())
</script>

<section class="sc-logs">
  <h3>{t('logs.server_log')}</h3>
  <p class="sc-logs__hint">{t('logs.what_the_log_is')}</p>

  <dl class="sc-logs__figures">
    <div>
      <dt>{t('logs.stored_size')}</dt>
      <dd>{storedLabel}</dd>
    </div>
    <div>
      <dt>{t('logs.segments')}</dt>
      <dd>{formatNumber(snap.segments)}</dd>
    </div>
  </dl>

  <!-- `onsubmit` rather than a click handler on the button: Enter in any
       field is then the same action, and the slice's `refresh` skips the
       settling window because a submit is already a deliberate commit. -->
  <form
    class="sc-logs__filters"
    onsubmit={(e) => {
      e.preventDefault()
      store.refresh()
    }}
  >
    <!-- A real fieldset, so the four boxes are announced as one group called
         "Level" rather than as four unrelated checkboxes. -->
    <fieldset class="sc-logs__levels">
      <legend>{t('logs.level')}</legend>
      <div class="sc-logs__level-boxes">
        {#each ALL_LOG_LEVELS as level (level)}
          <Checkbox
            checked={filters.levels.has(level)}
            label={t(LEVEL_KEY[level])}
            onchange={() => changeFilter({ type: 'TOGGLE_LEVEL', level })}
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
        oninput={(v) => changeFilter({ type: 'SET_TEXT', text: v })}
        autocomplete="off"
      />
      <TextField
        label={t('logs.subsystem')}
        placeholder={t('logs.e_g_dav')}
        list="sc-logs-subsystems"
        value={filters.subsystem}
        oninput={(v) => changeFilter({ type: 'SET_SUBSYSTEM', subsystem: v })}
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
        oninput={(v) => changeFilter({ type: 'SET_REQUEST_ID', requestId: v })}
        autocomplete="off"
      />
      <TextField
        label={t('logs.from')}
        type="datetime-local"
        value={filters.since}
        oninput={(v) => changeFilter({ type: 'SET_SINCE', since: v })}
      />
      <TextField
        label={t('logs.to')}
        type="datetime-local"
        value={filters.until}
        oninput={(v) => changeFilter({ type: 'SET_UNTIL', until: v })}
      />
    </div>

    <Button variant="tonal" type="submit">{t('logs.apply_filters')}</Button>
  </form>

  <!-- The level, the message and the record's own metadata: the same in both
       row branches, so it lives in one place. -->
  {#snippet rowContent(r: AdminLogRecord)}
    <span class="sc-logs__level sc-logs__level--{r.level.toLowerCase()}">
      <Icon icon={icons[LEVEL_ICON[r.level] ?? 'info']} size={16} />
      {LEVEL_KEY[r.level] ? t(LEVEL_KEY[r.level]) : r.level}
    </span>
    <span class="sc-logs__body">
      <span class="sc-logs__msg">{r.msg}</span>
      <span class="sc-logs__meta">
        <span>{formatDateNs(r.ts_ns)}</span>
        {#if r.subsystem}<span>{r.subsystem}</span>{/if}
        {#if r.request_id}<span>{t('logs.request', { id: r.request_id })}</span>{/if}
      </span>
    </span>
  {/snippet}

  <!-- Loading and empty are announced, not only drawn. The region is always
       in the tree and only its text changes: a live region inserted with its
       message already inside is frequently not spoken, since there was no
       change for the reader to observe. Polite, because none of this
       interrupts what the reader is doing. -->
  <p class="sc-sr-only" role="status" aria-live="polite">
    {#if snap.loading}
      {t('logs.loading_logs')}
    {:else if snap.failed}
      {t('logs.could_not_load_logs')}
    {:else if records.length === 0}
      {t('logs.no_records_match')}
    {:else}
      {t('logs.showing_records', { count: records.length })}
    {/if}
  </p>

  {#if snap.loading}
    <ProgressCircular label={t('logs.loading_logs')} />
  {:else if snap.failed}
    <p class="sc-logs__error" role="alert">{t('logs.could_not_load_logs')}</p>
  {:else if records.length === 0}
    <div class="sc-logs__empty">
      <Icon icon={icons.list} size={28} />
      <p>{t('logs.no_records_match')}</p>
      <p class="sc-logs__empty-hint">{t('logs.widen_the_filters')}</p>
    </div>
  {:else}
    <ul class="sc-logs__list">
      {#each records as r, i (pureRecordKey(r, i))}
        {@const key = pureRecordKey(r, i)}
        {@const attrs = Object.entries(r.attrs)}
        {@const open = snap.expandedKey === key}
        <li class="sc-logs__item">
          <!-- The whole row is the disclosure control, so a record is one tab
               stop and Enter/Space opens it: a real `<button>`, not a div with
               a click handler and a role bolted on. A record with no
               attributes has nothing to disclose, so it is a plain `<div>`
               rather than a focusable control that does nothing.

               Two spelled-out branches rather than one `<svelte:element>`
               switching the tag: a dynamic tag defeats the compiler's own
               a11y analysis, so the version that read as clever was the one
               it could not check. The shared innards are a snippet, so there
               is still one copy of them. -->
          {#if attrs.length > 0}
            <button
              type="button"
              class="sc-logs__row sc-logs__row--button m3-layer sc-focus-ring"
              aria-expanded={open}
              aria-controls={`sc-log-attrs-${key}`}
              onclick={() => store.dispatch({ type: 'TOGGLE_EXPANDED', key })}
            >
              {@render rowContent(r)}
              <span class="sc-logs__disclose">
                {t('logs.attribute_count', { count: attrs.length })}
                <span class="sc-logs__chevron" class:sc-logs__chevron--open={open}>
                  <Icon icon={icons['chevron-right']} size={18} />
                </span>
              </span>
            </button>
          {:else}
            <div class="sc-logs__row">{@render rowContent(r)}</div>
          {/if}

          {#if open}
            <dl class="sc-logs__attrs" id={`sc-log-attrs-${key}`}>
              {#each attrs as [k, v] (k)}
                <div>
                  <dt>{k}</dt>
                  <dd>{v}</dd>
                </div>
              {/each}
            </dl>
          {/if}
        </li>
      {/each}
    </ul>

    {#if snap.truncated}
      <p class="sc-logs__note" role="status">
        {t('logs.reached_the_cap', { count: formatNumber(records.length) })}
      </p>
    {:else if snap.cursor !== ''}
      <div class="sc-logs__more">
        <Button variant="text" onclick={() => store.loadMore()} loading={snap.loadingMore}>
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
  .sc-logs h3 {
    margin: 0 0 8px;
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
    margin-bottom: 16px;
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
  /* Same reasoning as the audit log's filter row: `TextField` sizes its box
     off its own wrapper, so in a row each field has to be told what share it
     takes, and the set is capped so a filter form does not stretch across a
     1440px pane. */
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
  /* A `<button>` brings the UA's own background, border and font; the row is
     the control, so it keeps the row's box and the page's type. */
  .sc-logs__row--button {
    background: none;
    border: 0;
    font: inherit;
    cursor: pointer;
  }

  /* The level is never colour alone: the badge carries its own word and an
     icon, and colour is the third channel. Each pairing is a container role
     with its matching `on-` role, which is what holds the contrast in both
     themes without either being hand-tuned. */
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
  .sc-logs__level--error {
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
  /* A separator drawn by CSS rather than typed into the markup, so it is not
     read out as punctuation between every pair of fields. */
  .sc-logs__meta > span + span::before {
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
