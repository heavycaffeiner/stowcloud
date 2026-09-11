<script lang="ts">
  // Public share link page: a separate, lightweight
  // bundle. No admin UI, no auth flows, no upload machinery are imported
  // here on purpose, keeping this route's JS well under the 60 KB budget.
  import { t } from '../../../lib/i18n'
  import { goto } from '$app/navigation'
  import { page } from '$app/state'
  import { createQuery } from '@tanstack/svelte-query'
  import {
    dropUpload,
    getShare,
    shareDownloadUrl,
    shareZipUrl,
    unlockShare,
    ShareNotFoundError,
    SharePasswordRequiredError,
    SharePathGoneError,
    ShareTooLargeError
  } from '../../../lib/api/share'
  import { formatBytes } from '../../../lib/format/bytes'
  import Button from '../../../lib/ui/Button.svelte'
  import TextField from '../../../lib/ui/TextField.svelte'

  const token = $derived(page.params.token ?? '')
  // The subpath lives in the query string, so each folder is a history entry
  // and the browser's own back button is the way out. A reload works because
  // the server serves the SPA document for any `Accept: text/html` request to
  // `/s/{token}` regardless of the query.
  const subpath = $derived(page.url.searchParams.get('path') ?? '')

  const shareQuery = createQuery(() => ({
    queryKey: ['share', token, subpath],
    queryFn: () => getShare(token, subpath),
    // Definite link states (password required, expired, or gone paths) are not
    // retried automatically. A transient failure gets an explicit in-place
    // retry button below so the visitor can recover without losing context.
    retry: false
  }))
  const info = $derived(shareQuery.data ?? null)
  const loading = $derived(shareQuery.isPending)
  const needsPassword = $derived(shareQuery.error instanceof SharePasswordRequiredError)
  const retryableError = $derived.by(() => {
    const e = shareQuery.error
    return !!e && !needsPassword && !(e instanceof ShareNotFoundError) && !(e instanceof SharePathGoneError)
  })
  const error = $derived.by(() => {
    const e = shareQuery.error
    if (!e || needsPassword || e instanceof SharePathGoneError) return null
    return e instanceof ShareNotFoundError ? t('public_share.link_has_expired_or_does') : t('public_share.could_not_load')
  })

  let password = $state('')
  let unlocking = $state(false)
  let unlockError = $state<string | null>(null)


  // ── file drop (upload-only link) ──
  interface DropItem {
    file: File
    status: 'pending' | 'uploading' | 'done' | 'error'
    /** The name the server *stored* it under: a collision comes back
     *  renamed (`sc-core::links::unique_name`), and the uploader has no other
     *  way to learn which file is theirs: a drop link lists nothing. */
    storedAs: string
    /** Kept as a cause rather than a rendered string so a locale switch
     *  after the upload still shows the message in the current language. */
    failure: 'too_large' | 'failed' | null
  }
  let fileInput = $state<HTMLInputElement | null>(null)
  let queue = $state<DropItem[]>([])
  let uploading = $state(false)
  const doneCount = $derived(queue.filter((i) => i.status === 'done').length)

  function onPick(e: Event): void {
    const el = e.currentTarget as HTMLInputElement
    const picked = Array.from(el.files ?? [])
    // Clear it so re-picking the same file after a failure still fires
    // `change`: the value is otherwise unchanged and the event never comes.
    el.value = ''
    if (picked.length === 0) return
    const limit = info?.maxUploadBytes ?? null
    queue = [
      ...queue,
      ...picked.map((file): DropItem => ({
        file,
        // Refused before the request, not after: the body limit layer can only
        // cut the stream once the whole file has already gone up the wire.
        status: limit !== null && file.size > limit ? 'error' : 'pending',
        storedAs: '',
        failure: limit !== null && file.size > limit ? 'too_large' : null
      }))
    ]
    void runQueue()
  }
  /** One request at a time: a drop link is a single-shot `POST` per file, and
   *  serialising keeps the "n / m" count honest. Files picked mid-run join the
   *  same pass because the loop re-checks the live queue after every await. */
  async function runQueue(): Promise<void> {
    if (uploading) return
    uploading = true
    try {
      let index = 0
      while (index < queue.length) {
        const item = queue[index++]
        if (item.status !== 'pending') continue
        item.status = 'uploading'
        try {
          item.storedAs = await dropUpload(token, item.file)
          item.status = 'done'
        } catch (e) {
          // One failure does not abandon the rest of the queue.
          item.status = 'error'
          item.failure = e instanceof ShareTooLargeError ? 'too_large' : 'failed'
        }
      }
    } finally {
      uploading = false
      // A failed row can be retried while another file is still uploading.
      // If the live cursor has already passed that row, start another pass
      // after this one releases the guard.
      if (queue.some((item) => item.status === 'pending')) void runQueue()
    }
  }

  function retryUpload(index: number): void {
    const item = queue[index]
    if (!item || item.status !== 'error') return
    item.status = 'pending'
    item.failure = null
    item.storedAs = ''
    void runQueue()
  }

  function removeFailedUpload(index: number): void {
    const item = queue[index]
    if (!item || item.status !== 'error') return
    queue = queue.filter((_, i) => i !== index)
  }

  /** A folder the server refused. Cleared to the link root rather than shown
   *  as an empty list, which would look like an empty folder.
   *
   *  Deliberately not reset by the query: a `SharePathGoneError` retriggers
   *  the redirect below on every fetch of the gone path, and clearing it
   *  here would make the message flash and vanish before anybody read it.
   *  It clears when the visitor navigates on purpose. */
  let pathGone = $state(false)

  $effect(() => {
    if (shareQuery.error instanceof SharePathGoneError) {
      pathGone = true
      goto(`/s/${encodeURIComponent(token)}`, { replaceState: true })
    }
  })

  /** The link's own label as the root, then one crumb per resolved segment. */
  const crumbs = $derived.by(() => {
    const parts = subpath ? subpath.split('/').filter(Boolean) : []
    return parts.map((name, i) => ({ name, path: parts.slice(0, i + 1).join('/') }))
  })

  function openFolder(path: string): void {
    pathGone = false
    goto(path ? `/s/${encodeURIComponent(token)}?path=${encodeURIComponent(path)}` : `/s/${encodeURIComponent(token)}`)
  }

  function childPath(name: string): string {
    return subpath ? `${subpath}/${name}` : name
  }

  /** Hoisted out of the listing: `info` is a nullable `$state`, and reading it
   *  through a narrowing check inside an `{#each}` body is not something the
   *  compiled template can carry. */
  const canDownload = $derived(info?.canDownload ?? false)

  async function submitPassword(e: SubmitEvent): Promise<void> {
    e.preventDefault()
    unlockError = null
    unlocking = true
    try {
      const ok = await unlockShare(token, password)
      if (ok) {
        password = ''
        await shareQuery.refetch()
      } else {
        unlockError = t('common.incorrect_password')
      }
    } catch {
      unlockError = t('public_share.could_not_verify_try_again')
    } finally {
      unlocking = false
    }
  }

  /** One file. `path` is the link-relative path of the file, so the link's own
   *  target is `''` and a row inside a folder is the row's own path. */
  /** One file under the link. A plain navigation, like the folder zip: the
   *  response is the bytes, so there is nothing to await and nothing to fail
   *  before the browser takes over. */
  function download(path: string): void {
    window.location.href = shareDownloadUrl(token, path)
  }

  /** The folder currently being looked at, as a zip. A plain navigation: the
   *  response is the bytes, streamed, with no signed URL to fetch first. */
  function downloadFolder(): void {
    window.location.href = shareZipUrl(token, subpath)
  }
</script>

<svelte:head><title>{info?.label ?? info?.name ?? t('common.share_links')} - Stowcloud</title></svelte:head>

<div class="sc-share">
  <header class="sc-share__header">
    <strong>Stowcloud</strong> {t('public_share.public_share_link')}
  </header>

  {#if loading}
    <p class="sc-share__status">{t('common.loading')}</p>
  {:else if error}
    <p class="sc-share__status sc-share__status--error">{error}</p>
    {#if retryableError}
      <Button variant="outlined" loading={shareQuery.isFetching} onclick={() => shareQuery.refetch()}>
        {t('public_share.retry')}
      </Button>
    {/if}
  {:else if needsPassword}
    <form class="sc-share__unlock" onsubmit={submitPassword}>
      <p>{t('public_share.link_password_protected')}</p>
      <TextField type="password" label={t('common.password')} bind:value={password} error={unlockError} autofocus autocomplete="off" />
      <div class="sc-share__unlock-actions">
        <Button type="submit" variant="filled" disabled={!password} loading={unlocking}>
          {t('public_share.unlock')}
        </Button>
      </div>
    </form>
  {:else if info}
    <!-- The heading is the *link's* title and stays put; the breadcrumb below
         says where inside it the visitor is. Two things, not one: the link
         label is stable for the whole visit, and folding it into the crumb
         trail left the page with no heading at all, which is what a screen
         reader navigates by. -->
    <h1 class="sc-share__title">{info.label || info.name}</h1>
    {#if crumbs.length > 0}
      <nav class="sc-share__crumbs" aria-label={t('public_share.location')}>
        <ol>
          <li>
            <button type="button" class="sc-share__crumb" onclick={() => openFolder('')}>
              {t('public_share.top_folder')}
            </button>
          </li>
          {#each crumbs as crumb, i (crumb.path)}
            <li>
              <span class="sc-share__crumb-sep" aria-hidden="true">/</span>
              {#if i === crumbs.length - 1}
                <span aria-current="page">{crumb.name}</span>
              {:else}
                <button type="button" class="sc-share__crumb" onclick={() => openFolder(crumb.path)}>
                  {crumb.name}
                </button>
              {/if}
            </li>
          {/each}
        </ol>
      </nav>
    {/if}
    {#if pathGone}
      <p class="sc-share__status sc-share__status--error">{t('public_share.folder_gone')}</p>
    {/if}

    {#if info.isDrop}
      <p class="sc-share__status">{t('public_share.link_upload_only_nothing_can')}</p>
      <div class="sc-share__drop">
        <input class="sc-share__file" type="file" multiple bind:this={fileInput} onchange={onPick} />
        <Button variant="filled" disabled={uploading} onclick={() => fileInput?.click()}>
          {t('share_drop.pick_files')}
        </Button>
        {#if info.maxUploadBytes !== null}
          <p class="sc-share__status">{t('share_drop.limit_hint', { size: formatBytes(info.maxUploadBytes) })}</p>
        {/if}
      </div>
      {#if queue.length > 0}
        <p class="sc-share__status">{t('share_drop.uploading', { done: doneCount, total: queue.length })}</p>
        <ul class="sc-share__list">
          <!-- Append-only while uploading; failed rows can be removed and
               retain their own retry action without touching completed rows. -->
          {#each queue as item, i (i)}
            <li class="sc-share__row">
              <span class="sc-filename sc-share__name">{item.file.name}</span>
              <span class="sc-share__size" class:sc-share__status--error={item.status === 'error'}>
                {#if item.status === 'done'}
                  {t('share_drop.uploaded_as', { name: item.storedAs })}
                {:else if item.failure === 'too_large'}
                  {t('share_drop.too_large')}
                {:else if item.failure === 'failed'}
                  {t('share_drop.failed')}
                {:else if item.status === 'uploading'}
                  {t('share_drop.in_progress')}
                {:else}
                  {formatBytes(item.file.size)}
                {/if}
              </span>
              {#if item.status === 'error'}
                <div class="sc-share__row-actions">
                  <Button variant="text" onclick={() => retryUpload(i)}>{t('share_drop.retry')}</Button>
                  <Button variant="text" onclick={() => removeFailedUpload(i)}>{t('share_drop.remove')}</Button>
                </div>
              {/if}
            </li>
          {/each}
        </ul>
      {/if}
    {:else if !info.isDir}
      <p class="sc-share__status">{formatBytes(info.size)}</p>
      {#if info.canDownload}
        <Button variant="filled" onclick={() => download(subpath)}>
          {t('common.download')}
        </Button>
      {/if}
    {:else}
      <ul class="sc-share__list">
        {#each info.entries ?? [] as e (e.name)}
          <li class="sc-share__row">
            {#if e.kind === 'dir'}
              <button
                type="button"
                class="sc-filename sc-share__name sc-share__folder"
                aria-label={t('public_share.open_folder', { name: e.name })}
                onclick={() => openFolder(childPath(e.name))}
              >
                {e.name}
              </button>
              <span class="sc-share__size">-</span>
            {:else}
              <span class="sc-filename sc-share__name">{e.name}</span>
              <span class="sc-share__size">{formatBytes(e.size)}</span>
              {#if canDownload}
                <Button variant="text" onclick={() => download(childPath(e.name))}>
                  {t('common.download')}
                </Button>
              {/if}
            {/if}
          </li>
        {:else}
          <li class="sc-share__row sc-share__row--empty">{t('public_share.empty')}</li>
        {/each}
      </ul>
      {#if info.canDownload}
        <Button variant="filled" onclick={downloadFolder}>{t('public_share.download_folder')}</Button>
      {/if}
    {/if}
  {/if}
</div>

<style>
  .sc-share {
    max-width: 640px;
    margin: 0 auto;
    padding: var(--sc-page-pad);
  }
  .sc-share__header {
    margin-bottom: 32px;
    color: var(--m3c-on-surface-variant);
  }
  .sc-share__title {
    margin: 0 0 16px;
    @apply --m3-headline-small;
  }
  .sc-share__status {
    color: var(--m3c-on-surface-variant);
  }
  .sc-share__status--error {
    color: var(--m3c-error);
  }
  /* Stretch, not `flex-start`: the field box is `width: 100%` of a wrapper with
     `min-width: 0` (`TextField.svelte`), so a shrink-to-fit column left it a
     zero-width sliver of border with the label clipped to one syllable. Only
     the button wants its own size. */
  .sc-share__unlock {
    display: flex;
    flex-direction: column;
    gap: 16px;
    max-width: 320px;
  }
  /* The UA gives <p> a 1em block margin, and margins do not collapse in a flex
     container, so the first row sat 16px below the top of the form on top of
     the gap that already separates the rows. */
  .sc-share__unlock > p {
    margin: 0;
  }
  .sc-share__unlock-actions {
    align-self: flex-start;
  }
  .sc-share__drop {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: 8px;
    margin-bottom: 24px;
  }
  .sc-share__file {
    display: none;
  }
  .sc-share__list {
    list-style: none;
    margin: 0 0 24px;
    padding: 0;
    border: 1px solid var(--m3c-outline-variant);
    border-radius: var(--m3-shape-medium);
    overflow: hidden;
  }
  .sc-share__row {
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: 16px;
    padding: 12px 16px;
    border-bottom: 1px solid var(--m3c-outline-variant);
  }
  .sc-share__row-actions {
    display: flex;
    align-items: center;
    gap: 4px;
    flex-shrink: 0;
  }
  .sc-share__row:last-child {
    border-bottom: none;
  }
  .sc-share__row--empty {
    color: var(--m3c-on-surface-variant);
    justify-content: center;
  }
  .sc-share__name {
    /* A share is filled with entries neither this page nor its owner's
       upload chose the names of. Without `min-width: 0` here, this flex
       item's automatic minimum width is its *content's* min-content size --
       for Hangul that's small already (every syllable is a valid break
       point under UAX #14) but still non-zero, and `.sc-share__size`'s
       `flex-shrink: 0` claims space unconditionally, so a long enough
       unbroken name pushed this into single-syllable-per-line wrapping
       before `min-width: 0` let it shrink to fit the ellipsis instead. */
    min-width: 0;
    flex: 1 1 auto;
  }
  .sc-share__size {
    color: var(--m3c-on-surface-variant);
    flex-shrink: 0;
  }
  .sc-share__crumbs {
    margin-bottom: 16px;
  }
  .sc-share__crumbs ol {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 4px;
    margin: 0;
    padding: 0;
    list-style: none;
    @apply --m3-title-medium;
  }
  .sc-share__crumbs li {
    display: flex;
    align-items: center;
    gap: 4px;
    min-width: 0;
  }
  .sc-share__crumb {
    padding: 0 4px;
    border: none;
    border-radius: var(--m3-shape-extra-small);
    background: none;
    color: var(--m3c-primary);
    font: inherit;
    cursor: pointer;
  }
  .sc-share__crumb:hover {
    background: var(--m3c-surface-container);
  }
  .sc-share__crumb:focus-visible,
  .sc-share__folder:focus-visible {
    outline: 2px solid var(--m3c-primary);
    outline-offset: 2px;
  }
  .sc-share__crumb-sep {
    color: var(--m3c-on-surface-variant);
  }
  .sc-share__folder {
    padding: 0;
    border: none;
    background: none;
    color: var(--m3c-primary);
    font: inherit;
    text-align: start;
    cursor: pointer;
  }
</style>
