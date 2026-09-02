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
  import { formatDateNs, t } from '../i18n'
  import { formatBytes } from '../format/bytes'
  import type { Entry } from '../api/client'
  import type { BrowseState } from '../state/browse.svelte'
  import { selectionMeasure } from '../state/measure.svelte'
  import { uiState } from '../state/ui.svelte'
  import { Icon } from 'm3-svelte'
  import { icons } from '../icons'
  import Button from './Button.svelte'
  import IconButton from './IconButton.svelte'
  import ProgressCircular from './ProgressCircular.svelte'

  interface Props {
    browse: BrowseState
    onclose: () => void
  }

  let { browse, onclose }: Props = $props()

  let panelEl: HTMLElement | undefined = $state()

  /** Which of the three shapes the panel is showing. `selected` only resolves
   *  rows that are loaded, so a range reaching into an unfetched gap reports
   *  what it can see rather than a count it cannot back up. */
  const selection = $derived(browse.selected)
  const one = $derived(selection.length === 1 ? selection[0] : null)
  const many = $derived(selection.length > 1)

  const folderName = $derived.by((): string => {
    const parts = browse.path.split('/').filter(Boolean)
    return parts.length > 0 ? parts[parts.length - 1] : t('browse.home')
  })

  /** The directory holding a single selected entry, which is the directory
   *  being browsed. Shown with a leading slash so it reads as a path. */
  const location = $derived(browse.path.startsWith('/') ? browse.path : `/${browse.path}`)

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
        { key: 'count', label: t('details.items'), value: String(selection.length) },
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
      { key: 'folders', label: t('grid.folders'), value: String(browse.dirs) },
      { key: 'files', label: t('grid.files'), value: String(Math.max(0, browse.total - browse.dirs)) },
      { key: 'location', label: t('details.location'), value: location }
    ]
  })

  // ── recursive folder size ──
  //
  // Held in shared state and driven by the browse page, which is mounted
  // whether or not this panel is: the selection toolbar shows the same number.
  // Two drivers would alternate keys on every selection change and cancel each
  // other's pending walk.
  //
  // These two describe the same target the page targeted, and exist only so
  // the retry button has something to re-run.
  const sizeTargets = $derived.by((): string[] => {
    const here = location.replace(/^\/+/, '').replace(/\/+$/, '')
    if (selection.length === 0) return here ? [here] : []
    return selection.filter((e) => e.kind === 'dir').map((e) => (here ? `${here}/${e.name}` : e.name))
  })

  /** What the folders' sizes are added to: the files in the same selection,
   *  whose sizes the listing already carries. */
  const sizeBase = $derived(
    many
      ? { bytes: browse.totalSelectedSize, files: selection.length - browse.selectedFolderCount }
      : { bytes: 0, files: 0 }
  )

  const measured = $derived(selectionMeasure.state)

  const title = $derived(many ? t('details.multiple_selected', { count: selection.length }) : (one?.name ?? folderName))
  const titleIcon = $derived(many ? icons.check : one ? (one.kind === 'dir' ? icons.folder : icons.file) : icons.folder)

  // Sheet mode only. A column that steals focus the moment it opens would
  // take the keyboard away from the grid the user is still navigating.
  $effect(() => {
    if (!uiState.compact || !panelEl) return
    const previous = document.activeElement as HTMLElement | null
    panelEl.querySelector<HTMLElement>('button')?.focus()
    return () => previous?.focus()
  })

  function onKeydown(e: KeyboardEvent): void {
    if (!uiState.compact) return
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
  class:sc-details--sheet={uiState.compact}
  role={uiState.compact ? 'dialog' : 'complementary'}
  aria-modal={uiState.compact ? 'true' : undefined}
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
          <Button variant="text" onclick={() => selectionMeasure.retry(sizeTargets, sizeBase)}>
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
