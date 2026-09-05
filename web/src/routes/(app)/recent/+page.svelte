<script lang="ts">
  // /recent: what you did here, newest first.
  // Not "what changed on disk": these shares are also written by Samba and by
  // whatever else has the directory, and an mtime ordering over them is
  // dominated by writers nobody reading this screen has a relationship with.
  // The list is what this server recorded doing on your behalf, verified
  // against the filesystem row by row, so a file somebody else wrote is not
  // here and a file you moved is, at its new path.
  //
  // No virtualization, for the same reason the trash page has none: the row
  // count is capped server-side at 500. A row click navigates to the containing
  // folder, which is what a search result click already does: opening a
  // preview would cost a stat round trip per row for the full Entry the
  // dialog needs.
  import { goto } from '$app/navigation'
  import { createQuery } from '@tanstack/svelte-query'
  import { recentQuery } from '../../../lib/query/files'
  import type { RecentHit } from '../../../lib/api/types'
  import { describeApiError } from '../../../lib/api/error-text'
  import { formatDateNs, t } from '../../../lib/i18n'
  import { formatBytes } from '../../../lib/format/bytes'
  import { Icon } from 'm3-svelte'
  import { icons } from '../../../lib/icons'
  import IconButton from '../../../lib/ui/IconButton.svelte'
  import ProgressCircular from '../../../lib/ui/ProgressCircular.svelte'

  const recent = createQuery(() => recentQuery())
  const hits = $derived(recent.data?.hits ?? [])
  const loading = $derived(recent.isPending)
  const loadError = $derived(recent.error ? describeApiError(recent.error, t('recent.could_not_load')) : null)

  /** The folder holding a hit, as a browse path. */
  function parentOfVpath(vpath: string): string {
    const i = vpath.lastIndexOf('/')
    return i <= 0 ? vpath : vpath.slice(0, i)
  }

  function open(hit: RecentHit): void {
    goto(`/b/${parentOfVpath(hit.vpath)}`)
  }

  /** The one-word verb for what was done. Text, never a colour or an icon on
   *  its own: a restored file sits at the top of the list carrying a
   *  three-year-old modification time, and without the word that reads as a
   *  bug.
   *
   *  Spelled out rather than interpolated into the key, because a key built at
   *  runtime cannot be extracted and `tools/i18n-check.mjs` would have no call
   *  site to tie the catalogue entries to. A verb this build does not know
   *  still renders a word. */
  function verb(hit: RecentHit): string {
    switch (hit.op) {
      case 'edit':
        return t('recent.op_edit')
      case 'copy':
        return t('recent.op_copy')
      case 'move':
        return t('recent.op_move')
      case 'restore':
        return t('recent.op_restore')
      default:
        return t('recent.op_upload')
    }
  }
</script>

<svelte:head><title>{t('recent.title_stowcloud')}</title></svelte:head>

<div class="sc-recent">
  <div class="sc-recent__inner">
    <header class="sc-recent__header">
      <IconButton label={t('trash.go_back')} onclick={() => goto('/b')}>
        <Icon icon={icons['chevron-left']} />
      </IconButton>
      <h1>{t('nav.recent')}</h1>
      <IconButton label={t('common.refresh')} onclick={() => recent.refetch()}><Icon icon={icons.refresh} /></IconButton>
    </header>

    {#if loading}
      <div class="sc-recent__loading"><ProgressCircular /></div>
    {:else if loadError}
      <p class="sc-recent__error" role="alert">{loadError}</p>
    {:else if hits.length === 0}
      <p class="sc-recent__empty">{t('recent.nothing_recent')}</p>
    {:else}
      <ul class="sc-recent__list">
        {#each hits as hit (hit.vpath)}
          <li>
            <!-- The accessible name carries the folder too: a file name alone
                 repeats across folders, and a list of twelve "IMG_0042.jpg"
                 tells a screen-reader user nothing about which is which. -->
            <button
              type="button"
              class="sc-recent__row"
              aria-label={t('recent.open_containing_folder', {
                name: hit.name,
                folder: parentOfVpath(hit.vpath)
              })}
              onclick={() => open(hit)}
            >
              <span class="sc-recent__icon"><Icon icon={icons.file} size={20} /></span>
              <span class="sc-recent__text">
                <span class="sc-filename sc-recent__name">{hit.name}</span>
                <span class="sc-recent__path">{parentOfVpath(hit.vpath)}</span>
              </span>
              <span class="sc-recent__meta">{verb(hit)}</span>
              <span class="sc-recent__meta">{formatBytes(hit.size)}</span>
              <span class="sc-recent__meta">{formatDateNs(hit.at_ns)}</span>
            </button>
          </li>
        {/each}
      </ul>
    {/if}
  </div>
</div>

<style>
  .sc-recent {
    height: 100%;
    overflow-y: auto;
    word-break: keep-all;
  }
  .sc-recent__inner {
    max-width: min(720px, 100%);
    margin-inline: auto;
    padding: var(--sc-page-pad);
  }
  .sc-recent__header {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-bottom: 16px;
  }
  .sc-recent__header h1 {
    flex: 1;
    @apply --m3-headline-small;
  }
  .sc-recent__list {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .sc-recent__row {
    display: flex;
    align-items: center;
    gap: 12px;
    width: 100%;
    padding: 8px 16px;
    border: none;
    border-bottom: 1px solid var(--m3c-outline-variant);
    background: none;
    color: inherit;
    font: inherit;
    text-align: start;
    cursor: pointer;
  }
  .sc-recent__row:hover {
    background: var(--m3c-surface-container);
  }
  .sc-recent__row:focus-visible {
    outline: 2px solid var(--m3c-primary);
    outline-offset: -2px;
  }
  .sc-recent__icon {
    display: inline-flex;
    flex: none;
    color: var(--m3c-on-surface-variant);
  }
  .sc-recent__text {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
  }
  .sc-recent__name {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .sc-recent__path {
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .sc-recent__meta {
    flex-shrink: 0;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
    white-space: nowrap;
  }
  .sc-recent__loading {
    display: flex;
    justify-content: center;
    padding: 32px;
  }
  .sc-recent__error {
    color: var(--m3c-error);
    padding: 16px;
  }
  .sc-recent__empty {
    color: var(--m3c-on-surface-variant);
    padding: 32px;
    text-align: center;
  }

  /* Same rule the trash and browse lists follow: the fixed-width meta columns
     disappear before the name is squeezed into per-character wrapping. */
  @media (max-width: 480px) {
    .sc-recent__meta {
      display: none;
    }
  }
</style>
