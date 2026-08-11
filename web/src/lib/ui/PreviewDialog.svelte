<script lang="ts">
  // PreviewDialog.svelte — the full-screen viewer a file opens into.
  //
  // Clicking a file used to download it, which is a commitment: it writes to
  // the user's disk to answer "what is this". Looking is the common case, so
  // looking is what a click does now, and downloading moved to a button inside
  // here (and stayed on the action menus).
  //
  // Four bodies, decided per file: an image, text, an archive listing, or
  // neither. "Neither" is a real state with its own screen rather than an empty
  // box, because most of a NAS is video and this is what the viewer shows for
  // it.
  import { api, type Entry } from '../api/client'
  import type { ArchiveEntry } from '../api/types'
  import { formatBytes } from '../format/bytes'
  import { t } from '../i18n'
  import { Icon } from 'm3-svelte'
  import { icons } from '../icons'
  import IconButton from './IconButton.svelte'
  import Button from './Button.svelte'
  import ProgressCircular from './ProgressCircular.svelte'

  interface Props {
    open: boolean
    entry: Entry | null
    /** Virtual path of the entry, needed to read text content. */
    path: string
    hasPrev: boolean
    hasNext: boolean
    onclose: () => void
    onprev: () => void
    onnext: () => void
    ondownload: (entry: Entry) => void
    onedit: (entry: Entry) => void
  }

  let { open, entry, path, hasPrev, hasNext, onclose, onprev, onnext, ondownload, onedit }: Props =
    $props()

  /** Extensions worth showing as text. Deliberately a list rather than "try it
   *  and see": reading an unknown 4 GB blob to discover it is not text is the
   *  one mistake this screen must not make on a file server. */
  const TEXT_EXT = new Set([
    'txt', 'md', 'markdown', 'log', 'csv', 'tsv', 'json', 'yaml', 'yml', 'toml', 'ini', 'conf',
    'cfg', 'xml', 'html', 'htm', 'css', 'scss', 'js', 'ts', 'jsx', 'tsx', 'svelte', 'vue', 'rs',
    'go', 'py', 'rb', 'php', 'java', 'kt', 'c', 'h', 'cpp', 'hpp', 'cs', 'sh', 'bash', 'zsh',
    'sql', 'env', 'gitignore', 'dockerfile', 'makefile'
  ])
  /** Text is loaded into the browser whole, so it needs a ceiling. Past this
   *  the viewer offers the editor instead, which is the thing that already
   *  knows how to open something large. */
  const TEXT_MAX_BYTES = 2 * 1024 * 1024
  /** Long edge to ask the thumbnailer for. Sized for a full-screen viewer on a
   *  normal display rather than for the row icons the same endpoint feeds. */
  const PREVIEW_DIM: [number, number] = [1600, 1600]

  function extensionOf(name: string): string {
    const i = name.lastIndexOf('.')
    return i <= 0 ? name.toLowerCase() : name.slice(i + 1).toLowerCase()
  }

  type Body =
    | { kind: 'image' }
    | { kind: 'text' }
    | { kind: 'too-large-text' }
    | { kind: 'archive' }
    | { kind: 'none' }

  const body = $derived.by((): Body => {
    if (!entry) return { kind: 'none' }
    if (entry.preview?.available) return { kind: 'image' }
    // No size ceiling: the server reads a zip's metadata region and nothing
    // else, so what a listing costs follows the entry count, which file size
    // does not predict.
    if (extensionOf(entry.name) === 'zip') return { kind: 'archive' }
    if (!TEXT_EXT.has(extensionOf(entry.name))) return { kind: 'none' }
    return entry.size > TEXT_MAX_BYTES ? { kind: 'too-large-text' } : { kind: 'text' }
  })

  let imageUrl = $state<string | null>(null)
  let text = $state<string | null>(null)
  /** The archive's entries. Nothing here is clickable: opening one means
   *  extraction, which this server does not do. */
  let archive = $state<ArchiveEntry[] | null>(null)
  let loading = $state(false)
  let failed = $state<string | null>(null)

  // Keyed on the entry's identity, not on `open`: stepping to the next file
  // with the arrows keeps the dialog open and has to reload the body.
  $effect(() => {
    const target = entry
    const kind = body.kind
    imageUrl = null
    text = null
    archive = null
    failed = null
    if (!open || !target) return

    let cancelled = false
    loading = kind === 'image' || kind === 'text' || kind === 'archive'
    void (async () => {
      try {
        if (kind === 'image') {
          if (target.id === undefined) {
            // Same honest dead end `downloadEntry` documents: `/fs/link` is
            // fid-only and the fid is allocated lazily, so there is nothing to
            // ask for yet.
            throw new Error(t('preview.not_available'))
          }
          // `inline_thumb`, not `stream`: the thumbnail is the server's own
          // re-encode (`content_api.rs`), which is what makes it safe to serve
          // with an inline disposition at all. `stream` hands back the original
          // bytes, and pointing an `<img>` at attacker-supplied bytes on our
          // own origin is the XSS the split exists to prevent.
          const { url } = await api.link(target.id, 'inline_thumb', PREVIEW_DIM)
          if (!cancelled) imageUrl = url
        } else if (kind === 'text') {
          const res = await api.readFile(path)
          if (!cancelled) text = res.content
        } else if (kind === 'archive') {
          const res = await api.archiveList(path)
          if (!cancelled) archive = res.entries
        }
      } catch (err) {
        if (!cancelled) failed = err instanceof Error ? err.message : t('preview.failed')
      } finally {
        if (!cancelled) loading = false
      }
    })()
    return () => {
      cancelled = true
    }
  })

  function onKeydown(e: KeyboardEvent): void {
    if (!open) return
    if (e.key === 'Escape') {
      e.preventDefault()
      onclose()
    } else if (e.key === 'ArrowLeft' && hasPrev) {
      e.preventDefault()
      onprev()
    } else if (e.key === 'ArrowRight' && hasNext) {
      e.preventDefault()
      onnext()
    }
  }
</script>

<svelte:window onkeydown={onKeydown} />

{#if open && entry}
  <!-- `aria-modal` plus a real label: this covers the page, so a screen reader
       has to be told it is a dialog and what it is showing. -->
  <div class="sc-preview" role="dialog" aria-modal="true" aria-label={entry.name}>
    <header class="sc-preview__bar">
      <IconButton label={t('common.close')} onclick={onclose}><Icon icon={icons.close} /></IconButton>
      <span class="sc-preview__name" title={entry.name}>{entry.name}</span>
      <span class="sc-preview__size">{formatBytes(entry.size)}</span>
      <span class="sc-preview__gap"></span>
      {#if body.kind === 'text' || body.kind === 'too-large-text'}
        <IconButton label={t('browse.open_text_editor')} onclick={() => onedit(entry)}>
          <Icon icon={icons.rename} />
        </IconButton>
      {/if}
      <IconButton label={t('common.download')} onclick={() => ondownload(entry)}>
        <Icon icon={icons.download} />
      </IconButton>
    </header>

    <div class="sc-preview__body">
      <!-- Both columns are always rendered, and the one with no neighbour is
           hidden with `visibility`. `{#if}` removed the box, so the stage
           absorbed its width and the image recentred by half of it at the ends
           of a folder: stepping through with the arrow keys ended with the
           picture jumping sideways on its own. `visibility: hidden` keeps the
           box, takes the button out of the tab order and the accessibility
           tree, and receives no pointer events, so the tooltip cannot fire on
           an arrow that is not there. -->
      <div class="sc-preview__nav sc-preview__nav--prev" class:sc-preview__nav--empty={!hasPrev}>
        <IconButton label={t('preview.previous')} onclick={onprev}>
          <Icon icon={icons['chevron-left']} />
        </IconButton>
      </div>

      <div class="sc-preview__stage">
        {#if loading}
          <ProgressCircular />
        {:else if imageUrl}
          <img class="sc-preview__image" src={imageUrl} alt={entry.name} />
        {:else if text !== null}
          <pre class="sc-preview__text">{text}</pre>
        {:else if archive !== null}
          {@const entries = archive}
          <div class="sc-preview__archive">
            <p class="sc-preview__archive-count">{t('preview.archive_entries', { count: entries.length })}</p>
            {#if entries.length === 0}
              <p class="sc-preview__archive-empty">{t('preview.archive_empty')}</p>
            {:else}
              <ul class="sc-preview__archive-list">
                {#each entries as e (e.name)}
                  <li class="sc-preview__archive-row">
                    <span class="sc-filename sc-preview__archive-name">{e.name}</span>
                    <span class="sc-preview__archive-kind">
                      {e.kind === 'dir' ? t('details.folder') : t('details.file')}
                    </span>
                    <span class="sc-preview__archive-size">
                      {e.kind === 'dir' ? '' : formatBytes(e.size)}
                    </span>
                  </li>
                {/each}
              </ul>
            {/if}
          </div>
        {:else}
          <!-- Everything that cannot be shown lands here, and it is a card
               rather than bare text on the scrim: a message floating over a
               dimmed file list reads as a failure, a card reads as an answer
               with something to do next. One shape for "no preview for this
               kind", "too large" and "the read failed", because from here they
               are the same situation. -->
          <div class="sc-preview__card" role={failed ? 'alert' : undefined}>
            <p class="sc-preview__card-title">{t('preview.cannot_preview')}</p>
            <p class="sc-preview__card-reason">
              {#if failed}
                {failed}
              {:else if body.kind === 'too-large-text'}
                {t('preview.too_large_for_text')}
              {:else}
                {t('preview.no_preview')}
              {/if}
            </p>
            <div class="sc-preview__card-actions">
              <Button variant="filled" onclick={() => ondownload(entry)}>
                <Icon icon={icons.download} size={18} />
                {t('common.download')}
              </Button>
              {#if body.kind === 'too-large-text'}
                <Button variant="outlined" onclick={() => onedit(entry)}>
                  {t('browse.open_text_editor')}
                </Button>
              {/if}
            </div>
          </div>
        {/if}
      </div>

      <div class="sc-preview__nav sc-preview__nav--next" class:sc-preview__nav--empty={!hasNext}>
        <IconButton label={t('preview.next')} onclick={onnext}>
          <Icon icon={icons['chevron-right']} />
        </IconButton>
      </div>
    </div>
  </div>
{/if}

<style>
  .sc-preview {
    position: fixed;
    inset: 0;
    z-index: 30;
    display: flex;
    flex-direction: column;
    /* Translucent, so the listing stays visible underneath and the viewer
       reads as something on top of where you were rather than a new page. It
       was 82%, which left the file cards behind bright enough to be read, so
       the eye had two things competing for it and the name in the bar sat on
       whatever happened to be under it. */
    background: color-mix(in srgb, var(--m3c-scrim) 93%, transparent);
    /* `--m3c-scrim` is #000 in both themes, so whatever is drawn straight onto
       it has to be light in both themes. `--m3c-inverse-on-surface` is only
       the light half of its pair in the light theme; in the dark theme it is
       #2c322d, which put the title bar at a contrast ratio of 1.5:1 against
       the scrim. This picks whichever of the two tokens is the light one. */
    color: light-dark(var(--m3c-inverse-on-surface), var(--m3c-on-surface));
  }
  .sc-preview__bar {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 8px 16px;
    min-height: 56px;
    /* The bar has no surface of its own, so without this the name and size
       are legible only by luck of what sits behind them. */
    background: linear-gradient(
      to bottom,
      color-mix(in srgb, var(--m3c-scrim) 70%, transparent),
      transparent
    );
  }
  .sc-preview__name {
    @apply --m3-title-medium;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .sc-preview__size {
    @apply --m3-body-small;
    opacity: 0.8;
    flex: 0 0 auto;
  }
  .sc-preview__gap {
    flex: 1 1 auto;
  }
  .sc-preview__body {
    flex: 1 1 auto;
    display: flex;
    align-items: center;
    min-height: 0;
    padding: 0 8px 16px;
  }
  .sc-preview__nav {
    flex: 0 0 auto;
  }
  /* Keeps the box, so the stage's width and the image's centre never depend on
     where in the folder the viewer is. */
  .sc-preview__nav--empty {
    visibility: hidden;
  }
  .sc-preview__archive {
    display: flex;
    flex-direction: column;
    gap: 8px;
    width: 100%;
    max-width: 720px;
    max-height: 100%;
    padding: 16px;
    overflow: auto;
    color: light-dark(#fff, #fff);
  }
  .sc-preview__archive-count,
  .sc-preview__archive-empty {
    margin: 0;
    @apply --m3-body-small;
  }
  .sc-preview__archive-list {
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .sc-preview__archive-row {
    display: grid;
    grid-template-columns: 1fr auto auto;
    gap: 12px;
    align-items: center;
    padding: 8px 0;
    border-bottom: 1px solid rgb(255 255 255 / 12%);
    @apply --m3-body-medium;
  }
  .sc-preview__archive-name {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .sc-preview__archive-kind,
  .sc-preview__archive-size {
    @apply --m3-body-small;
    white-space: nowrap;
  }
  .sc-preview__stage {
    flex: 1 1 auto;
    min-width: 0;
    height: 100%;
    display: flex;
    align-items: center;
    justify-content: center;
    overflow: auto;
  }
  .sc-preview__image {
    max-width: 100%;
    max-height: 100%;
    object-fit: contain;
  }
  .sc-preview__text {
    @apply --m3-body-medium;
    width: 100%;
    height: 100%;
    margin: 0;
    padding: 16px;
    overflow: auto;
    white-space: pre-wrap;
    word-break: break-word;
    background: var(--m3c-surface);
    color: var(--m3c-on-surface);
    border-radius: 12px;
  }
  /* An elevated surface, not the scrim's own colours: this is the one part of
     the overlay a user is meant to read and act on, so it sits on the normal
     surface palette the rest of the app uses. */
  .sc-preview__card {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 8px;
    max-width: 480px;
    padding: 32px 24px;
    border-radius: 16px;
    background: var(--m3c-surface-container-high);
    color: var(--m3c-on-surface);
    text-align: center;
  }
  .sc-preview__card-title {
    @apply --m3-title-medium;
    margin: 0;
  }
  .sc-preview__card-reason {
    @apply --m3-body-medium;
    margin: 0;
    color: var(--m3c-on-surface-variant);
  }
  .sc-preview__card-actions {
    display: flex;
    flex-wrap: wrap;
    justify-content: center;
    gap: 12px;
    margin-top: 16px;
  }
</style>
