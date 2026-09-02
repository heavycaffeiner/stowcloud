<script lang="ts">
  // /trash —: list, restore, and permanently purge
  // trashed items. `GET/POST /api/trash[/restore|/purge]`
  // (`go/internal/httpapi/handler/trash.go`) existed and worked before this page did —
  // grep for "trash" across `web/src/` returned zero files. This is the
  // first thing in the app that ever calls them.
  //
  // Not linked from `NavigationBar`/`NavigationDrawer` yet (those are owned
  // by a different pass — `web/src/lib/ui/Navigation*.svelte`) beyond the
  // one entry point this page's own owner *does* get to add: the browse
  // page's More (overflow) menu, `(app)/b/[...path]/+page.svelte`.
  //
  // No virtualization: a trash listing is `<share>/.sctrash`'s flat
  // one directory read (`go/internal/httpapi/handler/trash.go`), not the 100k-row directory the
  // browse view has to handle — a plain list is the right tool here.
  import { goto } from '$app/navigation'
  import { api, ApiError, type TrashEntry } from '../../../lib/api/client'
  import { formatDateNs, t } from '../../../lib/i18n'
  import { formatBytes } from '../../../lib/format/bytes'
  import Button from '../../../lib/ui/Button.svelte'
  import Checkbox from '../../../lib/ui/Checkbox.svelte'
  import ConfirmDialog from '../../../lib/ui/ConfirmDialog.svelte'
  import { Icon } from 'm3-svelte'
  import { icons } from '../../../lib/icons'
  import IconButton from '../../../lib/ui/IconButton.svelte'
  import ProgressCircular from '../../../lib/ui/ProgressCircular.svelte'
  import Snackbar from '../../../lib/ui/Snackbar.svelte'

  let entries = $state<TrashEntry[]>([])
  let loading = $state(true)
  let loadError = $state<string | null>(null)
  let selected = $state<Set<string>>(new Set())
  let snackbarMsg = $state<string | null>(null)
  let purgeOpen = $state(false)
  /** `null` means "purge whatever's selected" — set to a single id when the
   *  confirm dialog was opened from one row's own Delete permanently button
   *  instead of the bulk toolbar action. */
  let purgeSingle = $state<string | null>(null)
  let busy = $state(false)

  async function load(): Promise<void> {
    loading = true
    loadError = null
    try {
      entries = await api.trashList()
      // Drop selection entries whose row no longer exists (e.g. a second
      // tab already restored/purged them).
      const known = new Set(entries.map((e) => e.id))
      selected = new Set([...selected].filter((id) => known.has(id)))
    } catch (err) {
      loadError = err instanceof ApiError ? err.message : t('trash.could_not_load_trash')
    } finally {
      loading = false
    }
  }

  $effect(() => {
    void load()
  })

  function toggle(id: string): void {
    const next = new Set(selected)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    selected = next
  }

  function selectAll(): void {
    selected = entries.length === selected.size ? new Set() : new Set(entries.map((e) => e.id))
  }

  /** Summarizes a batch `OpResult[]` the same honest way the browse page's
   *  own delete/copy flows should (and, for copy, now does — see
   *  `types.ts`'s `BatchItemResult` doc comment): a result that reports
   *  success is not evidence by itself when some items in the same batch
   *  failed, so a partial failure says exactly how many, not just "Done". */
  function summarize(results: { ok: boolean }[], verb: string): string {
    const failed = results.filter((r) => !r.ok).length
    if (failed === 0) return t('trash.items', { count: results.length, verb })
    const ok = results.length - failed
    return t('trash.succeeded_failed', { verb, ok, failed })
  }

  async function restore(ids: string[]): Promise<void> {
    if (ids.length === 0) return
    busy = true
    try {
      const res = await api.trashRestore(ids)
      snackbarMsg = summarize(res.results, t('trash.restored'))
      selected = new Set([...selected].filter((id) => !ids.includes(id)))
      await load()
    } catch (err) {
      snackbarMsg = err instanceof ApiError ? err.message : t('trash.could_not_restore')
    } finally {
      busy = false
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
    busy = true
    try {
      const res = await api.trashPurge(ids)
      snackbarMsg = summarize(res.results, t('trash.deleted_permanently'))
      selected = new Set([...selected].filter((id) => !ids.includes(id)))
      await load()
    } catch (err) {
      snackbarMsg = err instanceof ApiError ? err.message : t('trash.could_not_delete_permanently')
    } finally {
      busy = false
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
      <IconButton label={t('common.refresh')} onclick={() => load()}><Icon icon={icons.refresh} /></IconButton>
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
     wrapping — same rule the browse list/grid passes already established. */
  @media (max-width: 480px) {
    .sc-trash__meta {
      display: none;
    }
  }
</style>
