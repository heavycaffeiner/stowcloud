<script lang="ts">
  // Public share link page —: a separate, lightweight
  // bundle. No admin UI, no auth flows, no upload machinery are imported
  // here on purpose, keeping this route's JS well under the 60 KB budget.
  import { t } from '../../../lib/i18n'
  import { page } from '$app/state'
  import {
    dropUpload,
    getShare,
    requestShareDownload,
    unlockShare,
    ShareNotFoundError,
    SharePasswordRequiredError,
    ShareTooLargeError,
    type ShareInfo
  } from '../../../lib/api/share'
  import { formatBytes } from '../../../lib/format/bytes'
  import Button from '../../../lib/ui/Button.svelte'
  import TextField from '../../../lib/ui/TextField.svelte'

  const token = $derived(page.params.token ?? '')

  let info = $state<ShareInfo | null>(null)
  let error = $state<string | null>(null)
  let loading = $state(true)
  let needsPassword = $state(false)

  let password = $state('')
  let unlocking = $state(false)
  let unlockError = $state<string | null>(null)

  let downloading = $state(false)
  let downloadError = $state<string | null>(null)

  // ── file drop (upload-only link) ──
  interface DropItem {
    file: File
    status: 'pending' | 'uploading' | 'done' | 'error'
    /** The name the server *stored* it under — a collision comes back
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
    // `change` — the value is otherwise unchanged and the event never comes.
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
   *  same pass — the loop re-checks `queue.length` after every await. */
  async function runQueue(): Promise<void> {
    if (uploading) return
    uploading = true
    try {
      for (const item of queue) {
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
    }
  }

  let lastToken = ''
  async function load(): Promise<void> {
    loading = true
    error = null
    needsPassword = false
    try {
      info = await getShare(token)
    } catch (e) {
      if (e instanceof SharePasswordRequiredError) needsPassword = true
      else error = e instanceof ShareNotFoundError ? t('public_share.link_has_expired_or_does') : t('public_share.could_not_load')
    } finally {
      loading = false
    }
  }
  $effect(() => {
    if (token === lastToken) return
    lastToken = token
    void load()
  })

  async function submitPassword(e: SubmitEvent): Promise<void> {
    e.preventDefault()
    unlockError = null
    unlocking = true
    try {
      const ok = await unlockShare(token, password)
      if (ok) {
        password = ''
        await load()
      } else {
        unlockError = t('common.incorrect_password')
      }
    } catch {
      unlockError = t('public_share.could_not_verify_try_again')
    } finally {
      unlocking = false
    }
  }

  async function download(): Promise<void> {
    if (downloading) return
    downloading = true
    downloadError = null
    try {
      const url = await requestShareDownload(token)
      window.location.href = url
    } catch {
      downloadError = t('public_share.could_not_start_download_try')
    } finally {
      downloading = false
    }
  }
</script>

<svelte:head><title>{info?.label ?? info?.name ?? t('common.share_links')} · Stowcloud</title></svelte:head>

<div class="sc-share">
  <header class="sc-share__header">
    <strong>Stowcloud</strong> {t('public_share.public_share_link')}
  </header>

  {#if loading}
    <p class="sc-share__status">{t('common.loading')}</p>
  {:else if error}
    <p class="sc-share__status sc-share__status--error">{error}</p>
  {:else if needsPassword}
    <form class="sc-share__unlock" onsubmit={submitPassword}>
      <p>{t('public_share.link_password_protected')}</p>
      <TextField type="password" label={t('common.password')} bind:value={password} error={unlockError} autofocus autocomplete="off" />
      <div class="sc-share__unlock-actions">
        <Button type="submit" variant="filled" disabled={!password} loading={unlocking}>
          {t('common.ok')}
        </Button>
      </div>
    </form>
  {:else if info}
    <h1 class="sc-share__title">{info.label || info.name}</h1>

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
          <!-- Append-only, never reordered, so the index is a stable key. -->
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
            </li>
          {/each}
        </ul>
      {/if}
    {:else if !info.isDir}
      <p class="sc-share__status">{formatBytes(info.size)}</p>
      {#if info.canDownload}
        <Button variant="filled" onclick={download} loading={downloading}>{t('common.download')}</Button>
      {/if}
    {:else}
      <ul class="sc-share__list">
        {#each info.entries ?? [] as e (e.name)}
          <li class="sc-share__row">
            <span class="sc-filename sc-share__name">{e.name}</span>
            <span class="sc-share__size">{e.kind === 'file' ? formatBytes(e.size) : '—'}</span>
          </li>
        {:else}
          <li class="sc-share__row sc-share__row--empty">{t('public_share.empty')}</li>
        {/each}
      </ul>
      {#if info.canDownload}
        <Button variant="filled" onclick={download} loading={downloading}>{t('public_share.download_all')}</Button>
      {/if}
    {/if}
    {#if downloadError}<p class="sc-share__status sc-share__status--error">{downloadError}</p>{/if}
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
</style>
