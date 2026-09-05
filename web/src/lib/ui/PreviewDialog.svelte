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
  import { createQuery } from '@tanstack/svelte-query'
  import { api, type Entry } from '../api/client'
  import { ApiError, type ArchiveEntry, type ArchiveListing, type ShareEncryption } from '../api/types'
  import { describeApiError } from '../api/error-text'
  import { fileContentQuery, archiveEntriesQuery } from '../query/files'
  import { formatBytes } from '../format/bytes'
  import { t } from '../i18n'
  import { Icon } from 'm3-svelte'
  import { icons } from '../icons'
  import IconButton from './IconButton.svelte'
  import Button from './Button.svelte'
  import ProgressCircular from './ProgressCircular.svelte'
  import UnlockShareDialog from './UnlockShareDialog.svelte'
  import { IMAGE_EXT, VIDEO_EXT, extensionOf, mimeTypeOf } from './media-utils'
  import { registerMediaSource, releaseMediaSource, swReady } from '../crypto/download-sw'
  import { decryptDownload, isUnlocked, LockedSessionError, MAX_ENCRYPTABLE_BYTES } from '../crypto/e2ee'
  import { encryptionForLabel, shareLabelOf } from '../crypto/encrypted-shares'
  import { listEncryptedArchive } from '../crypto/zip-listing'

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
  /** Text is loaded into the browser whole, so it needs a ceiling. Past this
   *  the viewer offers the editor instead, which is the thing that already
   *  knows how to open something large. */
  const TEXT_MAX_BYTES = 2 * 1024 * 1024
  /** Long edge to ask the thumbnailer for. Sized for a full-screen viewer on a
   *  normal display rather than for the row icons the same endpoint feeds. */
  const PREVIEW_DIM: [number, number] = [1600, 1600]

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

  // Text and archive bodies are real reads; the key follows what is actually
  // shown, so switching to an image never keeps a stale text fetch running,
  // and each is disabled outright while the dialog is closed.
  const textQuery = createQuery(() => ({ ...fileContentQuery(entry), enabled: open && body.kind === 'text' }))

  // Whether the current entry's own share is end-to-end encrypted, resolved
  // once (encryptedShares(), encrypted-shares.ts, caches the whole set for
  // the session) before anything below decides how to fetch this entry's
  // bytes. `encryption` reads as `null` both while this is still pending and
  // once it resolves "not encrypted": every branch below that cares about
  // the difference also checks `encryptionPending`.
  const encryptionQuery = createQuery(() => ({
    queryKey: ['preview-encryption', entry?.path ?? ''],
    queryFn: () => encryptionForLabel(shareLabelOf((entry as Entry).path)),
    enabled: open && !!entry,
    staleTime: Infinity
  }))
  const encryption = $derived((encryptionQuery.data ?? null) as ShareEncryption | null)
  const encryptionPending = $derived(open && !!entry && encryptionQuery.isPending)

  /** Bumped after a successful unlock so `unlocked` re-reads `isUnlocked`,
   *  which is a plain function call rather than a reactive source Svelte
   *  would otherwise know to recompute on. */
  let unlockGeneration = $state(0)
  const unlocked = $derived.by((): boolean => {
    void unlockGeneration
    return encryption === null || isUnlocked(encryption.salt)
  })

  // Plain-share archive listing is disabled outright once this entry is
  // known to be encrypted, rather than left to run and 422: the server
  // holds no key for it. The encrypted counterpart is keyed on `unlocked`
  // too, so unlocking mid-preview refetches it instead of leaving a stale
  // LockedSessionError on screen.
  const archiveQuery = createQuery(() =>
    archiveEntriesQuery(path, open && body.kind === 'archive' && !encryptionPending && encryption === null)
  )
  const encryptedArchiveQuery = createQuery(() => ({
    queryKey: ['preview-encrypted-archive', entry?.path ?? '', unlocked],
    queryFn: () => listEncryptedArchive(entry as Entry, (encryption as ShareEncryption).salt),
    enabled: open && body.kind === 'archive' && encryption !== null && unlocked,
    staleTime: Infinity
  }))
  const archiveListing = $derived.by((): ArchiveListing | null => {
    if (body.kind !== 'archive') return null
    return (encryption ? encryptedArchiveQuery.data : archiveQuery.data) ?? null
  })
  const archiveError = $derived(encryption ? encryptedArchiveQuery.error : archiveQuery.error)
  const archivePending = $derived(encryption ? encryptedArchiveQuery.isPending : archiveQuery.isPending)

  /** The body needs bytes only the passphrase can decrypt, and does not
   *  have it yet. Rendering a broken `<img>`/`<video>` or an archive error
   *  here would be wrong (the file is not broken or missing, it is locked),
   *  so this is its own state rather than folding into `failed`. */
  const locked = $derived(
    !!entry &&
      encryption !== null &&
      !unlocked &&
      (body.kind === 'image' || body.kind === 'video' || body.kind === 'archive')
  )

  /** Raised automatically the moment a body turns out to be locked, the
   *  same proactive prompt a locked download raises
   *  (routes/(app)/b/[...path]/+page.svelte's own `openUnlockFor`), rather
   *  than waiting for the person to notice and ask for it. Left closed if
   *  they dismiss it without unlocking; the fallback card below offers a
   *  button to reopen it. */
  let unlockDialogOpen = $state(false)
  $effect(() => {
    if (locked) unlockDialogOpen = true
  })

  /** What identifies the body currently on screen, independent of object
   *  identity: what the image-fallback and archive-drilldown state below
   *  resets on. */
  const previewKey = $derived(open && entry ? `${body.kind}\x00${path}\x00${entry.id ?? ''}\x00${entry.size}` : null)

  let videoEl = $state<HTMLVideoElement | null>(null)
  /** Set once a preview URL fails to decode. Distinct from a fetch failure:
   *  nothing here is a query, since constructing the URL never round-trips. */
  let imageOverride = $state<string | null>(null)
  let imageGaveUp = $state(false)
  let videoGaveUp = $state(false)
  /** Where inside the archive the listing is, without a trailing slash. */
  let cwd = $state('')
  /** The encrypted image/video's own source, and how it was obtained:
   *  `mediaUrl` is a `/sc-media/<token>` Service Worker URL when one could
   *  be registered, or a `Blob` object URL for the buffered fallback below
   *  when it could not. `null`/`'idle'` for a plain share, where `imageUrl`/
   *  `videoUrl` read `api.*` directly instead. */
  let mediaUrl = $state<string | null>(null)
  let mediaKind = $state<'idle' | 'loading' | 'ready' | 'too-large' | 'no-worker-video' | 'failed'>('idle')

  $effect(() => {
    // The only dependency this effect is allowed to have: a change of
    // object identity with the same logical body must not reset anything.
    void previewKey
    imageOverride = null
    imageGaveUp = false
    videoGaveUp = false
    cwd = ''
    unlockDialogOpen = false
    return () => {
      videoEl?.pause()
    }
  })

  /**
   * Resolves `mediaUrl` for an encrypted image or video: registers a
   * reusable Range-capable Service Worker source when the worker is
   * available (`swReady()`, download-sw.ts) (the only path that can seek,
   * which a video needs), or, when it is not (this browser refused to run
   * it, which Chromium does for a self-signed certificate regardless of the
   * page's own settings), falls back to decrypting the whole file into a
   * `Blob` for an image under `MAX_ENCRYPTABLE_BYTES`. A video gets no such
   * fallback: without Range there is nothing to seek with, so this offers no
   * player at all rather than one that stalls on the first seek.
   */
  $effect(() => {
    void previewKey
    const currentEntry = entry
    mediaUrl = null
    mediaKind = 'idle'
    if (!currentEntry || (body.kind !== 'image' && body.kind !== 'video')) return
    if (encryptionPending) {
      mediaKind = 'loading'
      return
    }
    if (encryption === null || !unlocked) return // plain share, or the locked card above owns this

    let cancelled = false
    let token: string | null = null
    let objectUrl: string | null = null
    mediaKind = 'loading'
    const salt = encryption.salt
    const kind = body.kind

    void (async () => {
      const contentType = mimeTypeOf(currentEntry.name) ?? 'application/octet-stream'
      const reg = await swReady()
      if (cancelled) return
      if (reg?.active) {
        const registered = registerMediaSource(currentEntry, salt, contentType)
        token = registered.token
        mediaUrl = registered.url
        mediaKind = 'ready'
        return
      }
      if (kind === 'video') {
        mediaKind = 'no-worker-video'
        return
      }
      if (currentEntry.size > MAX_ENCRYPTABLE_BYTES) {
        mediaKind = 'too-large'
        return
      }
      try {
        const res = await fetch(api.contentUrl(currentEntry))
        if (!res.ok) throw new Error(`HTTP ${res.status}`)
        const bytes = new Uint8Array(await res.arrayBuffer())
        const plaintext = await decryptDownload(bytes, salt)
        if (cancelled) return
        objectUrl = URL.createObjectURL(new Blob([plaintext] as BlobPart[], { type: contentType }))
        mediaUrl = objectUrl
        mediaKind = 'ready'
      } catch {
        if (!cancelled) mediaKind = 'failed'
      }
    })()

    return () => {
      cancelled = true
      if (token) releaseMediaSource(token)
      if (objectUrl) URL.revokeObjectURL(objectUrl)
    }
  })

  const loading = $derived(
    (body.kind === 'text' && textQuery.isPending) ||
      (body.kind === 'archive' && !locked && (encryptionPending || archivePending)) ||
      ((body.kind === 'image' || body.kind === 'video') &&
        !locked &&
        encryption !== null &&
        (encryptionPending || mediaKind === 'loading'))
  )

  const imageUrl = $derived.by((): string | null => {
    if (!entry || body.kind !== 'image' || imageGaveUp) return null
    if (imageOverride) return imageOverride
    if (encryption !== null) return mediaKind === 'ready' ? mediaUrl : null
    // The row's own references, never a URL composed from its path: a
    // path the client joins is a path it can join wrongly, and one did.
    const ext = extensionOf(entry.name)
    if (entry.preview?.available && ext !== 'svg') {
      const url = api.thumbUrl(entry, PREVIEW_DIM[0])
      return url || api.contentUrl(entry)
    }
    return api.contentUrl(entry)
  })

  const videoUrl = $derived.by((): string | null => {
    if (!entry || body.kind !== 'video' || videoGaveUp) return null
    if (encryption !== null) return mediaKind === 'ready' ? mediaUrl : null
    return api.contentUrl(entry)
  })

  const text = $derived(body.kind === 'text' ? (textQuery.data?.content ?? null) : null)
  const archive = $derived(archiveListing?.entries ?? null)
  const skipped = $derived(archiveListing?.skipped ?? 0)
  const truncated = $derived(archiveListing?.truncated ?? false)

  const failed = $derived.by((): string | null => {
    if (body.kind === 'image' && imageGaveUp) return t('preview.cannot_preview')
    if (body.kind === 'video' && videoGaveUp) return t('preview.cannot_preview')
    if ((body.kind === 'image' || body.kind === 'video') && encryption !== null && !locked) {
      if (mediaKind === 'too-large') return t('preview.encrypted_too_large_to_buffer')
      if (mediaKind === 'no-worker-video') return t('preview.encrypted_video_needs_worker')
      if (mediaKind === 'failed') return t('preview.cannot_preview')
    }
    if (body.kind === 'text' && textQuery.error) return describeApiError(textQuery.error, t('preview.failed'))
    if (body.kind === 'archive' && !locked && archiveError) return describeApiError(archiveError, t('preview.failed'))
    return null
  })

  /** The server's own words, when it had any. Not translated. */
  const failedDetail = $derived.by((): string | null => {
    const err = body.kind === 'text' ? textQuery.error : body.kind === 'archive' ? archiveError : null
    return err instanceof ApiError && typeof err.detail?.reason === 'string' ? err.detail.reason : null
  })

  function onImageError(): void {
    if (encryption !== null) {
      // The media URL (or the decrypted Blob URL) already is the file's
      // real bytes, not a thumbnail with a full-content URL to fall back
      // to next: there is only the one reference for an encrypted entry.
      imageGaveUp = true
      return
    }
    // A preview that will not decode falls back to the file's own bytes,
    // once. Both URLs come from the row's own references, so the fallback is
    // a different reference rather than a differently composed path.
    const own = entry ? api.contentUrl(entry) : ''
    if (imageUrl && own && imageUrl !== own) {
      imageOverride = own
    } else {
      imageGaveUp = true
    }
  }

  function onVideoError(): void {
    videoGaveUp = true
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
              {#if truncated && archiveListing}
                <span class="sc-preview__archive-skipped">
                  {t('preview.archive_truncated', { limit: archiveListing.limit })}
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
            <p class="sc-preview__card-title">
              {locked ? t('preview.locked_title') : t('preview.cannot_preview')}
            </p>
            <p class="sc-preview__card-reason">
              {#if locked}
                {t('preview.locked_reason')}
              {:else if failed}
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
              {#if locked}
                <Button variant="filled" onclick={() => (unlockDialogOpen = true)}>
                  {t('encryption.unlock')}
                </Button>
              {:else}
                <Button variant="filled" onclick={() => ondownload(entry)}>
                  <Icon icon={icons.download} size={18} />
                  {t('common.download')}
                </Button>
                {#if body.kind === 'too-large-text'}
                  <Button variant="outlined" onclick={() => onedit(entry)}>
                    {t('browse.open_text_editor')}
                  </Button>
                {/if}
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

<UnlockShareDialog
  open={unlockDialogOpen}
  salt={encryption?.salt ?? ''}
  verifier={encryption?.verifier ?? ''}
  onunlock={() => {
    unlockDialogOpen = false
    unlockGeneration++
  }}
  onclose={() => (unlockDialogOpen = false)}
/>

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
