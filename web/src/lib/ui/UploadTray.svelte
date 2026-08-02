<script lang="ts">
  import { onDestroy } from 'svelte'
  import { fly, slide } from 'svelte/transition'
  import { cubicOut } from 'svelte/easing'
  import { uploadTray, type UploadItem } from '../state/upload-tray.svelte'
  import { t } from '../i18n'
  import { formatBytes, formatEta, formatRate } from '../format/bytes'
  import IconButton from './IconButton.svelte'
  import { Icon } from 'm3-svelte'
  import { icons } from '../icons'
  import ProgressLinear from './ProgressLinear.svelte'

  const totalActive = $derived(uploadTray.items.filter((i) => i.status === 'uploading' || i.status === 'paused').length)

  // --- Screen-reader announcements ------------------------------------------
  // DESIGN-FRONTEND.md §9 used to list this as a known gap: Snackbar was the
  // only aria-live region in the app and nothing routed uploads to it.
  //
  // Not reused from Snackbar: Snackbar is instantiated per-route (`/b/[...path]`,
  // `/trash`, `/edit/[...path]` each own a local `snackbarMsg` and their own
  // `<Snackbar>` element), while UploadTray is mounted once in
  // `(app)/+layout.svelte` and deliberately outlives route changes so an
  // upload survives navigation (§6). Piping upload announcements through
  // whichever page's Snackbar happens to be mounted right now would make them
  // go silent the moment the user navigates away from the page they started
  // the upload on — the exact thing "survives route changes" exists to avoid.
  // A second live region is the correct outcome here, not a shortcut.
  //
  // Two regions, polite and assertive, not one that swaps its own aria-live
  // value at runtime: several screen readers latch a live region's politeness
  // onto the element the first time they see it and don't re-read the
  // attribute on a later mutation, so a region that starts polite and gets
  // flipped to assertive can silently stay polite from then on.
  //
  // What gets announced, and why progress doesn't: sent/rate/etaSec repaint
  // up to 10 Hz (upload/worker.ts's PROGRESS_HZ_MS) — a live region that
  // spoke on every tick would re-interrupt itself faster than any sentence
  // could finish. Only status *transitions* are announced: queued→uploading
  // (batched — a folder drop queues dozens in the same tick, and announcing
  // each by name would still be mid-list when the batch had already
  // finished), →done, and →error. A transient retry (worker.ts posts an
  // 'error' with `retryIn` and keeps status 'uploading') is deliberately not
  // announced — only the terminal error state is, since a screen-reader user
  // can't act on "retrying" and doesn't need to be told about attempt 2 of 5.
  let politeMsg = $state('')
  let assertiveMsg = $state('')

  // Diffed against on every `items` mutation; not `$state` itself, since it
  // exists to compare, not to render.
  const lastStatus = new Map<string, UploadItem['status']>()
  const pendingStartIds = new Set<string>()
  let startTimer: ReturnType<typeof setTimeout> | undefined

  function say(target: 'polite' | 'assertive', text: string): void {
    // Clear, then set on the next tick: two files finishing with the same
    // resulting sentence (e.g. two uploads both named "IMG_0001.jpg") would
    // otherwise assign identical text twice in a row, which is not a DOM
    // mutation and so is silent the second time — aria-live fires on content
    // *change*, not on assignment.
    if (target === 'polite') {
      politeMsg = ''
      setTimeout(() => { politeMsg = text }, 0)
    } else {
      assertiveMsg = ''
      setTimeout(() => { assertiveMsg = text }, 0)
    }
  }

  function flushStarts(): void {
    startTimer = undefined
    const ids = [...pendingStartIds]
    pendingStartIds.clear()
    // Only announce "started" for files still actually in flight once the
    // batch window closes. A small file can finish (and already have
    // announced its own completion) inside the 150ms window — found live
    // against the dev server with a 28-byte file, which regularly beat the
    // timer. Without this filter, the batch fires *after* the completion
    // message and silently overwrites it back to "uploading", so a screen
    // reader user never hears the file actually finished.
    const names = ids
      .map((id) => uploadTray.items.find((i) => i.id === id))
      .filter((i): i is UploadItem => !!i && (i.status === 'uploading' || i.status === 'paused'))
      .map((i) => i.name)
    if (names.length === 0) return
    say(
      'polite',
      names.length === 1
        ? t('upload.uploading', { name: names[0] })
        : t('upload.uploading_files', { count: names.length })
    )
  }

  $effect(() => {
    const seenIds = new Set<string>()
    for (const item of uploadTray.items) {
      seenIds.add(item.id)
      const prev = lastStatus.get(item.id)
      if (prev === undefined) {
        pendingStartIds.add(item.id)
        startTimer ??= setTimeout(flushStarts, 150)
      } else if (prev !== 'done' && item.status === 'done') {
        say('polite', t('upload.finished_uploading', { name: item.name }))
      } else if (prev !== 'error' && item.status === 'error') {
        // Which file, and why — "upload failed" alone is useless with several
        // files in flight. `item.message` is a catalogue key the worker posted
        // (it runs off-thread with no locale state of its own), so the lookup
        // happens here.
        say(
          'assertive',
          `${t('upload.failed_upload', { name: item.name })} ${item.message ? t(item.message, item.messageParams) : ''}`.trim()
        )
      }
      lastStatus.set(item.id, item.status)
    }
    // Drop ids no longer present (dismissed/cleared) so this map doesn't grow
    // forever across a long session.
    for (const id of lastStatus.keys()) {
      if (!seenIds.has(id)) lastStatus.delete(id)
    }
  })

  onDestroy(() => clearTimeout(startTimer))

  // See Menu.svelte's comment: Svelte transitions (not a library) are the
  // mount/unmount animation mechanism here, and need their own
  // reduced-motion read since they can't consume the CSS duration tokens.
  function reduceMotion(): boolean {
    return typeof matchMedia === 'function' && matchMedia('(prefers-reduced-motion: reduce)').matches
  }
  function trayDuration(): number {
    return reduceMotion() ? 0 : 200
  }
</script>

<!-- Always mounted, independent of item count: a live region inserted into the
     DOM at the same moment it receives content is exactly the case some
     screen readers miss the announcement for. This exists for the app's
     whole session, same as UploadTray's underlying state (§6). -->
<div class="sc-upload-tray__sr-only" role="status" aria-live="polite" aria-atomic="true">{politeMsg}</div>
<div class="sc-upload-tray__sr-only" role="alert" aria-live="assertive" aria-atomic="true">{assertiveMsg}</div>

{#if uploadTray.items.length > 0}
  <div
    class="sc-upload-tray"
    class:sc-upload-tray--collapsed={!uploadTray.open}
    transition:fly={{ y: 32, duration: trayDuration(), easing: cubicOut }}
  >
    <div class="sc-upload-tray__header">
      <button class="sc-upload-tray__title" onclick={() => (uploadTray.open = !uploadTray.open)}>
        <Icon icon={icons.upload} size={18} />
        {t('common.upload')} {totalActive > 0 ? `(${totalActive})` : t('common.done')}
      </button>
      <div class="sc-upload-tray__actions">
        <IconButton label={t('common.clear_finished_items')} onclick={() => uploadTray.clearFinished()}>
          <Icon icon={icons.check} size={18} />
        </IconButton>
        <IconButton label={uploadTray.open ? t('common.collapse') : t('common.expand')} onclick={() => (uploadTray.open = !uploadTray.open)}>
          <Icon icon={icons[uploadTray.open ? 'chevron-right' : 'chevron-left']} size={18} />
        </IconButton>
      </div>
    </div>

    {#if uploadTray.open}
      <ul class="sc-upload-tray__list">
        {#each uploadTray.items as item (item.id)}
          <li class="sc-upload-tray__item" transition:slide={{ duration: trayDuration(), easing: cubicOut }}>
            <div class="sc-upload-tray__row">
              <span class="sc-filename sc-upload-tray__name">{item.name}</span>
              <span class="sc-upload-tray__meta">
                {formatBytes(item.sent)} / {formatBytes(item.total)}
                {#if item.status === 'uploading'} · {formatRate(item.rate)} · {formatEta(item.etaSec)}{/if}
              </span>
            </div>
            <ProgressLinear value={item.total > 0 ? item.sent / item.total : 0} label={item.name} />
            {#if item.message}<p class="sc-upload-tray__message">{t(item.message, item.messageParams)}</p>{/if}
            <div class="sc-upload-tray__controls">
              {#if item.status === 'uploading'}
                <IconButton label={t('upload.pause')} onclick={() => uploadTray.pause(item.id)}><Icon icon={icons.close} size={16} /></IconButton>
              {:else if item.status === 'paused'}
                <IconButton label={t('upload.resume')} onclick={() => uploadTray.resume(item.id)}><Icon icon={icons.upload} size={16} /></IconButton>
              {/if}
              {#if item.status === 'done' || item.status === 'canceled'}
                <IconButton label={t('common.clear')} onclick={() => uploadTray.dismiss(item.id)}><Icon icon={icons.close} size={16} /></IconButton>
              {:else}
                <IconButton label={t('common.cancel')} onclick={() => uploadTray.cancel(item.id)}><Icon icon={icons.close} size={16} /></IconButton>
              {/if}
            </div>
          </li>
        {/each}
      </ul>
    {/if}
  </div>
{/if}

<style>
  /* Same clip-to-1px technique as Switch.svelte's `.sc-switch__input` --
     content must reach a screen reader while never taking layout space or a
     visible paint. */
  .sc-upload-tray__sr-only {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip: rect(0 0 0 0);
  }
  .sc-upload-tray {
    /* Positioning (fixed/right/bottom/z-index) lives on `.sc-tray-stack`, the
       wrapper `(app)/+layout.svelte` renders around this and `JobTray` so the
       two stack in one corner instead of each claiming their own fixed spot. */
    width: min(360px, calc(100vw - 32px));
    max-height: 60vh;
    display: flex;
    flex-direction: column;
    border-radius: var(--m3-shape-large);
    background: var(--m3c-surface-container-high);
    color: var(--m3c-on-surface);
    box-shadow: var(--m3-elevation-3);
    overflow: hidden;
  }
  .sc-upload-tray__header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    height: 48px;
    padding-inline: 16px;
    border-bottom: 1px solid var(--m3c-outline-variant);
  }
  .sc-upload-tray--collapsed .sc-upload-tray__header {
    border-bottom: none;
  }
  .sc-upload-tray__title {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    border: none;
    background: transparent;
    color: inherit;
    font-weight: 600;
    cursor: pointer;
  }
  .sc-upload-tray__actions {
    display: flex;
    gap: 4px;
  }
  .sc-upload-tray__list {
    list-style: none;
    margin: 0;
    padding: 8px 16px;
    overflow-y: auto;
  }
  .sc-upload-tray__item {
    padding-block: 12px;
    border-bottom: 1px solid var(--m3c-outline-variant);
  }
  .sc-upload-tray__item:last-child {
    border-bottom: none;
  }
  .sc-upload-tray__row {
    display: flex;
    justify-content: space-between;
    gap: 12px;
    margin-bottom: 4px;
  }
  .sc-upload-tray__name {
    max-width: 180px;
    @apply --m3-body-medium;
  }
  .sc-upload-tray__meta {
    flex-shrink: 0;
    @apply --m3-body-small;
    color: var(--m3c-on-surface-variant);
  }
  .sc-upload-tray__message {
    margin: 4px 0 0;
    @apply --m3-body-small;
    color: var(--m3c-tertiary);
  }
  .sc-upload-tray__controls {
    display: flex;
    justify-content: flex-end;
    gap: 4px;
    margin-top: 4px;
  }
</style>
