<script lang="ts">
  // Trash page: list, restore, and permanently purge trashed items.
  //
  // Not linked from NavigationBar or NavigationDrawer yet beyond the
  // browse page More menu.
  //
  // No virtualization: a trash listing is a flat directory read,
  // not the 100k-row directory the browse view handles.
  import { goto } from '$app/navigation'
  import { createQuery, createMutation } from '@tanstack/svelte-query'
  import { trashQuery, trashRestoreMutation, trashPurgeMutation } from '../../../lib/query/files'
  import { describeApiError } from '../../../lib/api/error-text'
  import { formatDateNs, t } from '../../../lib/i18n'
  import { formatBytes } from '../../../lib/format/bytes'
  import { selection } from '../../../lib/store/selection.store'
  import Button from '../../../lib/ui/Button.svelte'
  import Checkbox from '../../../lib/ui/Checkbox.svelte'
  import ConfirmDialog from '../../../lib/ui/ConfirmDialog.svelte'
  import { Icon } from 'm3-svelte'
  import { icons } from '../../../lib/icons'
  import IconButton from '../../../lib/ui/IconButton.svelte'
  import ProgressCircular from '../../../lib/ui/ProgressCircular.svelte'
  import Snackbar from '../../../lib/ui/Snackbar.svelte'

  selection.reset()

  const trash = createQuery(() => trashQuery())
  const entries = $derived(trash.data ?? [])
  const loading = $derived(trash.isPending)
  const loadError = $derived(trash.error ? describeApiError(trash.error, t('trash.could_not_load_trash')) : null)
  const selected = $derived(selection.state.names)

  // A second tab restoring or purging a row shows up here as a refetch: drop
  // any selected id the fresh list no longer carries.
  $effect(() => {
    const known = new Set(entries.map((e) => e.id))
    const pruned = new Set([...selected].filter((id) => known.has(id)))
    if (pruned.size !== selected.size) selection.replace(pruned)
  })

  const restoreMutation = createMutation(() => trashRestoreMutation())
  const purgeMutation = createMutation(() => trashPurgeMutation())
  const busy = $derived(restoreMutation.isPending || purgeMutation.isPending)

  let snackbarMsg = $state<string | null>(null)
  let purgeOpen = $state(false)
  // null means purge whatever is selected: set to single id when opened from row
  let purgeSingle = $state<string | null>(null)

  function toggle(id: string): void {
    selection.toggle(id)
  }

  function selectAll(): void {
    if (entries.length > 0 && selected.size === entries.length) selection.clear()
    else selection.all(entries.map((e) => e.id))
  }

  /** Summarizes a batch OpResult array with total and failure counts. */
  function summarize(results: { ok: boolean }[], verb: string): string {
    const failed = results.filter((r) => !r.ok).length
    if (failed === 0) return t('trash.items', { count: results.length, verb })
    const ok = results.length - failed
    return t('trash.succeeded_failed', { verb, ok, failed })
  }

  async function restore(ids: string[]): Promise<void> {
    if (ids.length === 0) return
    try {
      const res = await restoreMutation.mutateAsync(ids)
      snackbarMsg = summarize(res.results, t('trash.restored'))
      selection.replace([...selected].filter((id) => !ids.includes(id)))
    } catch (err) {
      snackbarMsg = describeApiError(err, t('trash.could_not_restore'))
    }
  }

  function requestPurge(id: string | null): void {
    purgeSingle = id
    purgeOpen = true
  }

  async function confirmPurge(): Promise<void> {
    const ids = purgeSingle ? [purgeSingle] : [...selected]
    purgeOpen = false
    if (ids.length === 0) return
    try {
      const res = await purgeMutation.mutateAsync(ids)
      snackbarMsg = summarize(res.results, t('trash.deleted_permanently'))
      selection.replace([...selected].filter((id) => !ids.includes(id)))
    } catch (err) {
      snackbarMsg = describeApiError(err, t('trash.could_not_delete_permanently'))
    } finally {
      purgeSingle = null
    }
  }

  const purgeCount = $derived(purgeSingle ? 1 : selected.size)
</script>

<svelte:head><title>{t('trash.trash_stowcloud')}</title></svelte:head>

<div class="sc-trash">
  <div class="sc-trash__inner">
    <header class="sc-trash__header">
      <IconButton label={t('trash.go_back')} onclick={() => goto('/b')}><Icon icon={icons['chevron-left']} /></IconButton>
      <h1>{t('common.trash')}</h1>
      <IconButton label={t('common.refresh')} onclick={() => trash.refetch()}><Icon icon={icons.refresh} /></IconButton>
    </header>

    {#if entries.length > 0}
      <div class="sc-trash__toolbar">
        <Checkbox
          checked={entries.length > 0 && selected.size === entries.length}
          indeterminate={selected.size > 0 && selected.size < entries.length}
          label={t('trash.select_all', { selected: selected.size, total: entries.length })}
          onchange={selectAll}
        />
        <div class="sc-trash__toolbar-actions">
          <!-- `trash.restore`/`trash.purge`, not `trash.restored`/
               `trash.deleted_permanently`: those two are the past-tense verb
               `summarize` drops into "{verb} {count} items" after the fact,
               and reusing them here labelled the buttons "Restored" and
               "Deleted permanently" before anything had been. Korean reads
               the same either way, which is why it survived this long. -->
          <Button variant="text" disabled={selected.size === 0 || busy} onclick={() => restore([...selected])}>
            {#snippet icon()}<Icon icon={icons.restore} size={18} />{/snippet}
            {t('trash.restore')}
          </Button>
          <Button variant="text" danger disabled={selected.size === 0 || busy} onclick={() => requestPurge(null)}>
            {#snippet icon()}<Icon icon={icons.trash} size={18} />{/snippet}
            {t('trash.purge')}
          </Button>
        </div>
      </div>
    {/if}

    {#if loading}
      <div class="sc-trash__loading"><ProgressCircular /></div>
    {:else if loadError}
      <p class="sc-trash__error" role="alert">{loadError}</p>
    {:else if entries.length === 0}
      <p class="sc-trash__empty">{t('trash.trash_empty')}</p>
    {:else}
      <ul class="sc-trash__list">
        {#each entries as entry (entry.id)}
          <li class="sc-trash__row">
            <Checkbox
              checked={selected.has(entry.id)}
              label={t('common.select', { name: entry.name })}
              hideLabel
              onchange={() => toggle(entry.id)}
            />
            <span class="sc-trash__name" title={entry.name}>{entry.name}</span>
            {#if !entry.is_dir}<span class="sc-trash__meta">{formatBytes(entry.size)}</span>{/if}
            <span class="sc-trash__meta">{t('trash.deleted', { date: formatDateNs(entry.deleted_at_ns) })}</span>
            <div class="sc-trash__row-actions">
              <IconButton label={t('trash.restore')} disabled={busy} onclick={() => restore([entry.id])}><Icon icon={icons.restore} /></IconButton>
              <IconButton label={t('trash.purge')} disabled={busy} onclick={() => requestPurge(entry.id)}><Icon icon={icons.trash} /></IconButton>
            </div>
          </li>
        {/each}
      </ul>
    {/if}
  </div>
</div>

<ConfirmDialog
  open={purgeOpen}
  title={t('trash.delete_permanently')}
  message={t('trash.permanently_deletes_items_cannot_undone', {
    count: purgeCount
  })}
  confirmLabel={t('trash.purge')}
  onclose={() => {
    purgeOpen = false
    purgeSingle = null
  }}
  onconfirm={confirmPurge}
/>
<Snackbar message={snackbarMsg} ondismiss={() => (snackbarMsg = null)} />

<style>
  .sc-trash {
    height: 100%;
    overflow-y: auto;
    word-break: keep-all;
  }
  .sc-trash__inner {
    max-width: min(720px, 100%);
    margin-inline: auto;
    padding: var(--sc-page-pad);
  }
  .sc-trash__header {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-bottom: 16px;
  }
  .sc-trash__header h1 {
    flex: 1;
    @apply --m3-headline-small;
  }
  .sc-trash__toolbar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    flex-wrap: wrap;
    gap: 8px;
    padding: 8px 16px;
    margin-bottom: 8px;
    border-radius: var(--m3-shape-medium);
    background: var(--m3c-surface-container);
  }
  .sc-trash__toolbar-actions {
    display: flex;
    align-items: center;
    gap: 4px;
  }
  .sc-trash__list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
  }
  .sc-trash__row {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 8px 16px;
    border-bottom: 1px solid var(--m3c-outline-variant);
  }
  .sc-trash__name {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .sc-trash__meta {
    flex-shrink: 0;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
    white-space: nowrap;
  }
  .sc-trash__row-actions {
    display: flex;
    align-items: center;
    flex-shrink: 0;
  }
  .sc-trash__loading {
    display: flex;
    justify-content: center;
    padding: 32px;
  }
  .sc-trash__error {
    color: var(--m3c-error);
    padding: 16px;
  }
  .sc-trash__empty {
    color: var(--m3c-on-surface-variant);
    padding: 32px;
    text-align: center;
  }

  /* 360px no-horizontal-overflow: the row's fixed-width meta columns must be
     free to disappear before the name is squeezed into per-character
     wrapping (the same rule the browse list/grid passes already established). */
  @media (max-width: 480px) {
    .sc-trash__meta {
      display: none;
    }
  }
</style>
