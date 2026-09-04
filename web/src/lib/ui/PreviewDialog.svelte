<script lang="ts">
  // PreviewDialog.svelte: the full-screen viewer a file opens into.
  //
  // Clicking a file used to download it, which is a commitment: it writes to
  // the user's disk to answer "what is this". Looking is the common case, so
  // looking is what a click does now, and downloading moved to a button inside
  // here (and stayed on the action menus).
  //
  // Five bodies, decided per file: an image, a video, text, an archive listing, or
  // neither. "Neither" is a real state with its own screen rather than an empty
  // box.
  import { untrack } from 'svelte'
  import { api, type Entry } from '../api/client'
  import { ApiError, type ArchiveEntry } from '../api/types'
  import { describeApiError } from '../api/error-text'
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

  /** Extensions worth showing as text. */
  const TEXT_EXT: Record<string, true> = {
    txt: true, md: true, markdown: true, log: true, csv: true, tsv: true, json: true, yaml: true, yml: true, toml: true, ini: true, conf: true,
    cfg: true, xml: true, html: true, htm: true, css: true, scss: true, js: true, ts: true, jsx: true, tsx: true, svelte: true, vue: true, rs: true,
    go: true, py: true, rb: true, php: true, java: true, kt: true, c: true, h: true, cpp: true, hpp: true, cs: true, sh: true, bash: true, zsh: true,
    sql: true, env: true, gitignore: true, dockerfile: true, makefile: true
  }
  const IMAGE_EXT: Record<string, true> = {
    jpg: true, jpeg: true, png: true, gif: true, webp: true, svg: true,
    bmp: true, ico: true, avif: true, tif: true, tiff: true
  }
  const VIDEO_EXT: Record<string, true> = {
    mp4: true, webm: true, ogg: true, mov: true, mkv: true, avi: true,
    m4v: true, flv: true, wmv: true, '3gp': true
  }
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
    | { kind: 'video' }
    | { kind: 'text' }
    | { kind: 'too-large-text' }
    | { kind: 'archive' }
    | { kind: 'none' }

  const body = $derived.by((): Body => {
    if (!entry) return { kind: 'none' }
    const ext = extensionOf(entry.name)
    if (ext in VIDEO_EXT) return { kind: 'video' }
    if (ext in IMAGE_EXT || entry.preview?.available) return { kind: 'image' }
    // else, so what a listing costs follows the entry count, which file size
    // does not predict.
    if (ext === 'zip') return { kind: 'archive' }
    if (!(ext in TEXT_EXT)) return { kind: 'none' }
    return entry.size > TEXT_MAX_BYTES ? { kind: 'too-large-text' } : { kind: 'text' }
  })

  let imageUrl = $state<string | null>(null)
  let videoUrl = $state<string | null>(null)
  let videoEl = $state<HTMLVideoElement | null>(null)
  let text = $state<string | null>(null)
  /** Every entry in the archive, flat, exactly as the server read it out of
   *  the central directory. What gets rendered is one directory of it at a
   *  time: see `level`. */
  let archive = $state<ArchiveEntry[] | null>(null)
  /** Where inside the archive the listing is, without a trailing slash. */
  let cwd = $state('')
  /** How many entries the server left out of the listing because their names
   *  are not safe to hand out. Usually zero. */
  let skipped = $state(0)
  let loading = $state(false)
  let failed = $state<string | null>(null)
  /** The server's own words, when it had any. Not translated. */
  let failedDetail = $state<string | null>(null)

  /** What identifies the body to load, as a string. */
  const loadKey = $derived(
    open && entry ? `${body.kind}\x00${path}\x00${entry.id ?? ''}\x00${entry.size}` : null
  )
  /** Plain, not `$state`: this is the effect's own bookkeeping, and making it
   *  reactive would make the effect depend on what it writes. */
  let loadedKey: string | null = null

  $effect(() => {
    const key = loadKey
    if (key === loadedKey) return
    loadedKey = key

    // Everything below is read untracked. `loadKey` is the only dependency
    // this effect is allowed to have; reading `entry` or `body` here would
    // reintroduce the object identity the key exists to avoid.
    const { target, kind, vpath } = untrack(() => ({
      target: entry,
      kind: body.kind,
      vpath: path
    }))

    imageUrl = null
    videoUrl = null
    text = null
    archive = null
    cwd = ''
    skipped = 0
    failed = null
    failedDetail = null
    // `loadKey` is null exactly when there is nothing to show, so this is the
    // closed case and the no-entry case at once, without reading either back.
    if (key === null || !target) return

    let cancelled = false
    loading = kind === 'image' || kind === 'video' || kind === 'text' || kind === 'archive'
    void (async () => {
      try {
        if (kind === 'image') {
          // The row's own references, never a URL composed from its path: a
          // path the client joins is a path it can join wrongly, and one did.
          const ext = extensionOf(target.name)
          if (target.preview?.available && ext !== 'svg') {
            const url = api.thumbUrl(target, PREVIEW_DIM[0])
            if (!cancelled) imageUrl = url || api.contentUrl(target)
          } else {
            if (!cancelled) imageUrl = api.contentUrl(target)
          }
        } else if (kind === 'video') {
          if (!cancelled) videoUrl = api.contentUrl(target)
        } else if (kind === 'text') {
          const res = await api.readFile(target)
          if (!cancelled) text = res.content
        } else if (kind === 'archive') {
          const res = await api.archiveList(vpath)
          if (!cancelled) {
            archive = res.entries
            skipped = res.skipped ?? 0
          }
        }
      } catch (err) {
        if (!cancelled) {
          failed = describeApiError(err, t('preview.failed'))
          failedDetail =
            err instanceof ApiError && typeof err.detail?.reason === 'string'
              ? err.detail.reason
              : null
        }
      } finally {
        if (!cancelled) loading = false
      }
    })()
    return () => {
      cancelled = true
      if (videoEl) {
        videoEl.pause()
      }
    }
  })

  function onImageError(): void {
    // A preview that will not decode falls back to the file's own bytes,
    // once. Both URLs come from the row's references, so the fallback is a
    // different reference rather than a differently composed path.
    const own = entry ? api.contentUrl(entry) : ''
    if (imageUrl && own && imageUrl !== own) {
      imageUrl = own
    } else {
      imageUrl = null
      failed = t('preview.cannot_preview')
    }
  }

  function onVideoError(): void {
    videoUrl = null
    failed = t('preview.cannot_preview')
  }

  /** One row of the archive at the directory currently open. */
  interface ArchiveRow {
    /** Path inside the archive, no trailing slash. What descending uses. */
    path: string
    /** The last segment, which is all a row shows. */
    label: string
    kind: 'dir' | 'file'
    size: number
  }

  function levelOf(entries: ArchiveEntry[], at: string): ArchiveRow[] {
    const prefix = at === '' ? '' : `${at}/`
    const dirs = new Map<string, ArchiveRow>()
    const files: ArchiveRow[] = []
    for (const e of entries) {
      const isDir = e.kind === 'dir'
      const full = isDir ? e.name.slice(0, -1) : e.name
      if (!full.startsWith(prefix)) continue
      const rest = full.slice(prefix.length)
      if (rest === '') continue
      const cut = rest.indexOf('/')
      if (cut === -1) {
        if (isDir) {
          dirs.set(rest, { path: full, label: rest, kind: 'dir', size: 0 })
        } else {
          files.push({ path: full, label: rest, kind: 'file', size: e.size })
        }
      } else {
        const label = rest.slice(0, cut)
        if (!dirs.has(label)) {
          dirs.set(label, { path: `${prefix}${label}`, label, kind: 'dir', size: 0 })
        }
      }
    }
    const byLabel = (a: ArchiveRow, b: ArchiveRow): number =>
      a.label.localeCompare(b.label, undefined, { numeric: true, sensitivity: 'base' })
    return [...[...dirs.values()].sort(byLabel), ...files.sort(byLabel)]
  }

  const level = $derived(archive ? levelOf(archive, cwd) : [])
  /** One entry per segment of `cwd`, each carrying the path to jump back to. */
  const crumbs = $derived(
    cwd === ''
      ? []
      : cwd.split('/').map((label, i, all) => ({ label, path: all.slice(0, i + 1).join('/') }))
  )

  function enter(row: ArchiveRow): void {
    if (row.kind === 'dir') cwd = row.path
  }

  function up(): void {
    const cut = cwd.lastIndexOf('/')
    cwd = cut === -1 ? '' : cwd.slice(0, cut)
  }

  function onKeydown(e: KeyboardEvent): void {
    if (!open) return
    if (e.key === 'Escape') {
      e.preventDefault()
      if (archive !== null && cwd !== '') up()
      else onclose()
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
      <div class="sc-preview__nav sc-preview__nav--prev" class:sc-preview__nav--empty={!hasPrev}>
        <IconButton label={t('preview.previous')} onclick={onprev}>
          <Icon icon={icons['chevron-left']} />
        </IconButton>
      </div>

      <div class="sc-preview__stage">
        {#if loading}
          <ProgressCircular />
        {:else if videoUrl}
          <div class="sc-preview__video-container">
            <video
              bind:this={videoEl}
              class="sc-preview__video"
              src={videoUrl}
              controls
              autoplay
              playsinline
              preload="metadata"
              onerror={onVideoError}
            >
              <track kind="captions" />
              {t('preview.cannot_preview')}
            </video>
          </div>
        {:else if imageUrl}
          <img class="sc-preview__image" src={imageUrl} alt={entry.name} onerror={onImageError} />
        {:else if text !== null}
          <pre class="sc-preview__text">{text}</pre>
        {:else if archive !== null}
          {@const entries = archive}
          <div class="sc-preview__archive">
            <p class="sc-preview__archive-count">
              {t('preview.archive_entries', { count: entries.length })}
              {#if skipped > 0}
                <span class="sc-preview__archive-skipped">
                  {t('preview.archive_skipped', { count: skipped })}
                </span>
              {/if}
            </p>
            <nav class="sc-preview__crumbs" aria-label={t('preview.archive_location')}>
              <button
                type="button"
                class="sc-preview__crumb"
                disabled={cwd === ''}
                onclick={() => (cwd = '')}
              >
                {entry.name}
              </button>
              {#each crumbs as c, i (c.path)}
                <span class="sc-preview__crumb-sep" aria-hidden="true">/</span>
                <button
                  type="button"
                  class="sc-preview__crumb"
                  disabled={i === crumbs.length - 1}
                  onclick={() => (cwd = c.path)}
                >
                  {c.label}
                </button>
              {/each}
            </nav>
            {#if level.length === 0}
              <p class="sc-preview__archive-empty">{t('preview.archive_empty')}</p>
            {:else}
              <ul class="sc-preview__archive-list">
                {#if cwd !== ''}
                  <li>
                    <button type="button" class="sc-preview__archive-row sc-preview__archive-row--up" onclick={up}>
                      <Icon icon={icons['chevron-left']} size={18} />
                      <span class="sc-preview__archive-name">{t('preview.archive_up')}</span>
                    </button>
                  </li>
                {/if}
                {#each level as row (row.path)}
                  <li>
                    {#if row.kind === 'dir'}
                      <button
                        type="button"
                        class="sc-preview__archive-row sc-preview__archive-row--dir"
                        onclick={() => enter(row)}
                      >
                        <Icon icon={icons.folder} size={18} />
                        <span class="sc-filename sc-preview__archive-name">{row.label}</span>
                        <span class="sc-preview__archive-kind">{t('details.folder')}</span>
                        <span class="sc-preview__archive-size"></span>
                      </button>
                    {:else}
                      <div class="sc-preview__archive-row">
                        <Icon icon={icons.file} size={18} />
                        <span class="sc-filename sc-preview__archive-name">{row.label}</span>
                        <span class="sc-preview__archive-kind">{t('details.file')}</span>
                        <span class="sc-preview__archive-size">{formatBytes(row.size)}</span>
                      </div>
                    {/if}
                  </li>
                {/each}
              </ul>
            {/if}
          </div>
        {:else}
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
            {#if failedDetail}
              <p class="sc-preview__card-detail">{failedDetail}</p>
            {/if}
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
    background: color-mix(in srgb, var(--m3c-scrim) 93%, transparent);
    color: light-dark(var(--m3c-inverse-on-surface), var(--m3c-on-surface));
  }
  .sc-preview__bar {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 8px 16px;
    min-height: 56px;
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
    grid-template-columns: auto 1fr auto auto;
    gap: 12px;
    align-items: center;
    width: 100%;
    padding: 8px 0;
    border: 0;
    border-bottom: 1px solid rgb(255 255 255 / 12%);
    background: none;
    color: inherit;
    font: inherit;
    text-align: start;
    @apply --m3-body-medium;
  }
  .sc-preview__archive-row--dir,
  .sc-preview__archive-row--up {
    cursor: pointer;
  }
  .sc-preview__archive-row--dir:hover,
  .sc-preview__archive-row--up:hover {
    background: rgb(255 255 255 / 8%);
  }
  .sc-preview__archive-row:focus-visible,
  .sc-preview__crumb:focus-visible {
    outline: 2px solid #fff;
    outline-offset: -2px;
  }
  .sc-preview__archive-row--up {
    grid-template-columns: auto 1fr;
  }
  .sc-preview__crumbs {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 4px;
    @apply --m3-body-small;
  }
  .sc-preview__crumb {
    max-width: 240px;
    overflow: hidden;
    padding: 4px;
    border: 0;
    border-radius: var(--m3-shape-extra-small);
    background: none;
    color: inherit;
    font: inherit;
    text-overflow: ellipsis;
    white-space: nowrap;
    cursor: pointer;
  }
  .sc-preview__crumb:hover:not(:disabled) {
    background: rgb(255 255 255 / 12%);
  }
  .sc-preview__crumb:disabled {
    cursor: default;
    opacity: 0.75;
  }
  .sc-preview__crumb-sep {
    opacity: 0.5;
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
  .sc-preview__video-container {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 100%;
    height: 100%;
    overflow: hidden;
  }
  .sc-preview__video {
    max-width: 100%;
    max-height: 100%;
    border-radius: 8px;
    box-shadow: 0 4px 24px rgb(0 0 0 / 40%);
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
  .sc-preview__card-detail {
    @apply --m3-body-small;
    margin: 0;
    color: var(--m3c-on-surface-variant);
    font-family: var(--m3-font-mono, ui-monospace, monospace);
    overflow-wrap: anywhere;
  }
  .sc-preview__archive-skipped {
    color: var(--m3c-error);
  }
  .sc-preview__card-actions {
    display: flex;
    flex-wrap: wrap;
    justify-content: center;
    gap: 12px;
    margin-top: 16px;
  }
</style>
