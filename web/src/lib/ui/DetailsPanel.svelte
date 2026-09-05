<script lang="ts">
  // DetailsPanel.svelte: what is known about whatever the browse page is
  // currently pointed at.
  //
  // Only fields `Entry` actually carries are shown. There is no owner, no
  // activity history and no sharing state here, because the listing does not
  // return any of it and inventing a request per selection change to fill a
  // side panel is a cost the panel does not earn. Share links are managed from
  // the row menu, which is where the data for them lives.
  //
  // At a wide window this is a sibling column, not a dialog: it takes no focus
  // when it opens and traps none while it is open. Below the MD3 compact
  // breakpoint there is no room for a column, so it becomes a sheet over the
  // listing, and a thing that covers the page has to behave like a dialog:
  // focus goes in, Tab stays in, Escape closes.
  import { createQueries } from '@tanstack/svelte-query'
  import { formatDateNs, t } from '../i18n'
  import { formatBytes } from '../format/bytes'
  import { ApiError, type Entry } from '../api/client'
  import { joinPath } from '../api/path-utils'
  import { folderSizeQuery } from '../query/files'
  import { ui } from '../store/ui.store'
  import { Icon } from 'm3-svelte'
  import { icons } from '../icons'
  import Button from './Button.svelte'
  import IconButton from './IconButton.svelte'
  import ProgressCircular from './ProgressCircular.svelte'

  interface Props {
    /** Absolute vpath of the directory being browsed. */
    path: string
    /** The loaded rows the selection resolves against; empty when nothing is
     *  selected. Only ever holds rows the page has actually fetched, same as
     *  the old `browse.selected`. */
    selected: readonly Entry[]
    /** Rows in the whole directory, for the zero-selection folder summary. */
    total: number
    /** How many of those rows are folders. */
    dirs: number
    onclose: () => void
  }

  let { path, selected, total, dirs, onclose }: Props = $props()

  let panelEl: HTMLElement | undefined = $state()

  const one = $derived(selected.length === 1 ? selected[0] : null)
  const many = $derived(selected.length > 1)

  const folderName = $derived.by((): string => {
    const parts = path.split('/').filter(Boolean)
    return parts.length > 0 ? parts[parts.length - 1] : t('browse.home')
  })

  /** The directory holding a single selected entry, which is the directory
   *  being browsed. Shown with a leading slash so it reads as a path. */
  const location = $derived(path.startsWith('/') ? path : `/${path}`)

  interface Field {
    key: string
    label: string
    value: string
  }

  function permissionSummary(e: Entry): string {
    const granted: string[] = []
    if (e.perms.download) granted.push(t('common.download'))
    if (e.perms.rename) granted.push(t('details.perm_rename'))
    if (e.perms.move) granted.push(t('details.perm_move'))
    if (e.perms.delete) granted.push(t('details.perm_delete'))
    if (e.perms.share) granted.push(t('details.perm_share'))
    return granted.length > 0 ? granted.join(', ') : t('details.perm_read_only')
  }

  const fields = $derived.by((): Field[] => {
    if (many) {
      // No size row here: the measurement below reports it, whether or not the
      // selection holds a folder. Two rows would be two answers to one
      // question, and the subtotal would be the wrong one.
      return [
        { key: 'count', label: t('details.items'), value: String(selected.length) },
        { key: 'location', label: t('details.location'), value: location }
      ]
    }
    if (one) {
      const out: Field[] = [
        { key: 'type', label: t('details.type'), value: one.kind === 'dir' ? t('details.folder') : t('details.file') }
      ]
      if (one.kind !== 'dir') out.push({ key: 'size', label: t('details.size'), value: formatBytes(one.size) })
      out.push({ key: 'modified', label: t('details.modified'), value: formatDateNs(one.mtime_ns) })
      out.push({ key: 'location', label: t('details.location'), value: location })
      if (one.link) {
        out.push({ key: 'link', label: t('details.symlink_target'), value: one.link.target })
      }
      out.push({ key: 'perms', label: t('details.permissions'), value: permissionSummary(one) })
      return out
    }
    return [
      { key: 'type', label: t('details.type'), value: t('details.folder') },
      { key: 'folders', label: t('grid.folders'), value: String(dirs) },
      { key: 'files', label: t('grid.files'), value: String(Math.max(0, total - dirs)) },
      { key: 'location', label: t('details.location'), value: location }
    ]
  })

  // ── recursive folder size ──
  //
  // One `folderSizeQuery` per selected folder (or the browsed folder itself,
  // when nothing is selected), combined into one figure. The cache is shared
  // with the selection toolbar on the browse page, so a second reader of the
  // same folder costs nothing and a walk already running is never repeated.
  // Absolute paths, the same spelling the listing and the selection toolbar
  // use, so the two readers share one cache entry per folder and a change
  // reported for that folder invalidates this measurement too.
  const sizeTargets = $derived.by((): string[] => {
    if (selected.length === 0) return path === '/' ? [] : [path]
    return selected.filter((e) => e.kind === 'dir').map((e) => joinPath(path, e.name))
  })

  /** What the folders' sizes are added to: the files in the same selection,
   *  whose sizes the listing already carries. */
  const sizeBase = $derived.by((): { bytes: number; files: number } => {
    if (!many) return { bytes: 0, files: 0 }
    let bytes = 0
    let files = 0
    for (const e of selected) {
      if (e.kind !== 'dir') {
        bytes += e.size
        files += 1
      }
    }
    return { bytes, files }
  })

  type Measured =
    | { kind: 'idle' }
    | { kind: 'measuring' }
    | { kind: 'done'; bytes: number; files: number }
    | { kind: 'failed'; reason: 'denied' | 'other' }

  /** Captured by the last `combine` run, purely so the retry button has
   *  something to call: `createQueries` returns the combined value, not the
   *  per-query handles, and refetching is the one thing combining loses. */
  let lastResults: { refetch: () => void }[] = []

  const measured = createQueries(() => ({
    queries: sizeTargets.map((p) => folderSizeQuery(p)),
    combine: (results): Measured => {
      lastResults = results
      if (results.length === 0) {
        return sizeBase.bytes > 0 || sizeBase.files > 0 ? { kind: 'done', ...sizeBase } : { kind: 'idle' }
      }
      const failed = results.find((r) => r.isError)
      if (failed) {
        const denied =
          failed.error instanceof ApiError && (failed.error.code === 'acl.denied' || failed.error.code === 'fs.denied')
        return { kind: 'failed', reason: denied ? 'denied' : 'other' }
      }
      if (results.some((r) => r.isPending)) return { kind: 'measuring' }
      const totals = results.reduce(
        (acc, r) => ({ bytes: acc.bytes + (r.data?.bytes ?? 0), files: acc.files + (r.data?.files ?? 0) }),
        sizeBase
      )
      return { kind: 'done', ...totals }
    }
  }))

  function retryMeasure(): void {
    for (const r of lastResults) void r.refetch()
  }

  const title = $derived(many ? t('details.multiple_selected', { count: selected.length }) : (one?.name ?? folderName))
  const titleIcon = $derived(many ? icons.check : one ? (one.kind === 'dir' ? icons.folder : icons.file) : icons.folder)

  // Sheet mode only. A column that steals focus the moment it opens would
  // take the keyboard away from the grid the user is still navigating.
  $effect(() => {
    if (!ui.state.compact || !panelEl) return
    const previous = document.activeElement as HTMLElement | null
    panelEl.querySelector<HTMLElement>('button')?.focus()
    return () => previous?.focus()
  })

  function onKeydown(e: KeyboardEvent): void {
    if (!ui.state.compact) return
    if (e.key === 'Escape') {
      e.stopPropagation()
      onclose()
      return
    }
    if (e.key !== 'Tab' || !panelEl) return
    const focusable = [...panelEl.querySelectorAll<HTMLElement>('button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])')].filter(
      (el) => !el.hasAttribute('disabled')
    )
    if (focusable.length === 0) return
    const first = focusable[0]
    const last = focusable[focusable.length - 1]
    if (e.shiftKey && document.activeElement === first) {
      e.preventDefault()
      last.focus()
    } else if (!e.shiftKey && document.activeElement === last) {
      e.preventDefault()
      first.focus()
    }
  }
</script>

<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
<aside
  bind:this={panelEl}
  class="sc-details"
  class:sc-details--sheet={ui.state.compact}
  role={ui.state.compact ? 'dialog' : 'complementary'}
  aria-modal={ui.state.compact ? 'true' : undefined}
  aria-label={t('details.title')}
  onkeydown={onKeydown}
>
  <header class="sc-details__head">
    <span class="sc-details__head-icon"><Icon icon={titleIcon} size={20} /></span>
    <h2 class="sc-details__title"><bdi>{title}</bdi></h2>
    <IconButton label={t('common.close')} onclick={onclose}>
      <Icon icon={icons.close} />
    </IconButton>
  </header>

  {#if one?.confusable}
    <p class="sc-details__warning">
      <Icon icon={icons.warning} size={16} />
      <span>{t('common.look_alike_characters')}</span>
    </p>
  {/if}

  <dl class="sc-details__fields">
    {#each fields as field (field.key)}
      <dt class="sc-details__label">{field.label}</dt>
      <dd class="sc-details__value"><bdi>{field.value}</bdi></dd>
    {/each}
    {#if measured.kind !== 'idle'}
      <dt class="sc-details__label">{many ? t('details.download_size') : t('details.total_size')}</dt>
      <dd class="sc-details__value">
        {#if measured.kind === 'done'}
          {formatBytes(measured.bytes)}
          <span class="sc-details__size-note">
            {t('details.size_file_count', { count: measured.files })}
          </span>
        {:else if measured.kind === 'failed'}
          <span class="sc-details__size-note" role="alert">
            {measured.reason === 'denied'
              ? t('details.size_hidden_by_permissions')
              : t('details.could_not_measure')}
          </span>
          <Button variant="text" onclick={retryMeasure}>
            {t('common.retry')}
          </Button>
        {:else}
          <!-- The screen never blocks on this: a cold walk on rotational
               storage is minutes, and the user can navigate away and come back
               to a cached answer. -->
          <span class="sc-details__size-progress">
            <ProgressCircular size={16} />
            {t('details.measuring')}
          </span>
        {/if}
      </dd>
    {/if}
  </dl>
</aside>

<style>
  .sc-details {
    box-sizing: border-box;
    flex: none;
    width: 320px;
    align-self: stretch;
    padding: 8px 16px 16px;
    border-inline-start: 1px solid var(--m3c-outline-variant);
    background: var(--m3c-surface);
    color: var(--m3c-on-surface);
    overflow-y: auto;
  }
  .sc-details--sheet {
    position: fixed;
    inset: 0;
    z-index: 30;
    width: auto;
    border-inline-start: none;
    background: var(--m3c-surface-container);
    padding-bottom: calc(16px + env(safe-area-inset-bottom, 0px));
  }
  .sc-details__head {
    display: flex;
    align-items: center;
    gap: 8px;
    min-height: 56px;
  }
  .sc-details__head-icon {
    display: inline-flex;
    flex: none;
    color: var(--m3c-primary);
  }
  .sc-details__title {
    flex: 1;
    min-width: 0;
    margin: 0;
    @apply --m3-title-medium;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .sc-details__warning {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 0 0 8px;
    padding: 8px 12px;
    border-radius: var(--m3-shape-small);
    background: var(--m3c-error-container);
    color: var(--m3c-on-error-container);
    @apply --m3-body-small;
  }
  .sc-details__fields {
    display: grid;
    grid-template-columns: minmax(0, 1fr);
    gap: 0;
    margin: 0;
  }
  .sc-details__label {
    @apply --m3-label-medium;
    color: var(--m3c-on-surface-variant);
    padding-block-start: 16px;
  }
  .sc-details__value {
    @apply --m3-body-medium;
    margin: 0;
    padding-block-start: 4px;
    overflow-wrap: anywhere;
  }
  .sc-details__size-note {
    display: block;
    @apply --m3-body-small;
    color: var(--m3c-on-surface-variant);
  }
  .sc-details__size-progress {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    @apply --m3-body-small;
    color: var(--m3c-on-surface-variant);
  }
</style>
