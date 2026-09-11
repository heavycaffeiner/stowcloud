<script lang="ts">
  // PathPickerDialog: browses the server's own filesystem so a path field
  // can be filled by pointing rather than by typing a guess. The listing
  // comes from the same sandboxed walk the server enforces on the typed
  // value anyway (`browseHostPath` / `browseSetupPath`), so what this dialog
  // shows is exactly what a correct typed path would have had to be.
  import { untrack } from 'svelte'
  import { createQuery } from '@tanstack/svelte-query'
  import { Icon } from 'm3-svelte'
  import { api } from '../api/client'
  import { describeApiError } from '../api/error-text'
  import { icons } from '../icons'
  import { t } from '../i18n'
  import Button from './Button.svelte'
  import Dialog from './Dialog.svelte'

  interface Props {
    open: boolean
    mode: 'folder' | 'file'
    /** The value currently typed, used as the opening location. Browsed
     *  directly in folder mode; its parent directory in file mode, since a
     *  file is not itself a place the server can list. */
    start?: string
    /** First-run setup has no session yet, so it browses through the setup
     *  token instead of the admin listing endpoint. */
    token?: string
    onclose: () => void
    onpick: (path: string) => void
  }

  let { open, mode, start, token, onclose, onpick }: Props = $props()

  function guessStart(): string {
    if (!start) return ''
    if (mode === 'folder') return start
    const cut = start.lastIndexOf('/')
    return cut > 0 ? start.slice(0, cut) : ''
  }

  let currentPath = $state('')
  let initialGuess = $state('')
  let selected = $state<string | null>(null)

  // Reads `open` only: `start`/`mode` are read through `untrack` so editing
  // the bound field (which cannot happen while this modal is up, but a
  // caller could still change its own props) never resets the browser back
  // to the guessed location out from under someone mid-navigation. The reset
  // is meant to happen exactly once, at the moment the dialog opens.
  $effect(() => {
    if (open) {
      untrack(() => {
        const guess = guessStart()
        currentPath = guess
        initialGuess = guess
        selected = null
      })
    }
  })

  function navigate(path: string) {
    currentPath = path
    selected = null
  }

  const listing = createQuery(() => ({
    queryKey: ['host-fs', token ?? null, currentPath],
    queryFn: () => (token ? api.browseSetupPath(token, currentPath) : api.browseHostPath(currentPath)),
    // Keeps the previous listing on screen while the next one loads, rather
    // than dropping straight to nothing: the native `<dialog>` this sits in
    // treats a click target that gets torn out of the document mid-click as
    // a click *outside* itself and light-dismisses, which a synchronous
    // reset to a "loading" placeholder would trigger on every single click
    // of a folder row. `(prev) => prev` is `keepPreviousData`, inlined so
    // this file does not reach past its declared `@tanstack/svelte-query`
    // dependency into the `query-core` package that only ships it.
    placeholderData: (prev) => prev,
    enabled: open,
    retry: false
  }))

  // A typed path is frequently one that does not exist yet, or one this
  // account cannot read; opening the dialog straight onto it would just
  // replace the field's own text with the dialog's error state for
  // something nobody asked to see. The first failure at the guessed
  // location quietly falls back to the root listing instead of insisting on
  // it; a failure anywhere the user has actually navigated to still shows.
  $effect(() => {
    if (open && listing.isError && currentPath === initialGuess && initialGuess !== '') {
      currentPath = ''
    }
  })

  const showError = $derived(listing.isError && (currentPath !== initialGuess || initialGuess === ''))
  const errorText = $derived(listing.error ? describeApiError(listing.error, t('picker.could_not_list')) : '')

  const atRoot = $derived(listing.data?.path === '')
  const hereText = $derived.by(() => {
    if (!listing.data) return ''
    return atRoot ? t('picker.roots') : `${t('picker.here')}: ${listing.data.path}`
  })

  // The root listing is a set of starting points, not a folder: it has no
  // path of its own, and handing back an empty one would clear the field the
  // picker was opened from.
  const canConfirm = $derived(
    mode === 'folder' ? !!listing.data && !listing.isError && !atRoot : selected !== null
  )

  // Two literal lookups rather than one built key: the catalogue check reads
  // call sites, and a key assembled at runtime is one it cannot see.
  const title = $derived(mode === 'folder' ? t('picker.title_folder') : t('picker.title_file'))

  function confirm() {
    if (mode === 'folder') {
      if (listing.data && listing.data.path !== '') onpick(listing.data.path)
    } else if (selected !== null) {
      onpick(selected)
    }
  }
</script>

<Dialog {open} {title} {onclose}>
  <div class="sc-picker">
    <div class="sc-picker__nav">
      <Button
        variant="text"
        disabled={!listing.data || listing.data.parent === ''}
        onclick={() => listing.data && navigate(listing.data.parent)}
      >
        {t('picker.up')}
      </Button>
      <!-- Announces both the navigated-to location and an incoming error
           without moving focus, the same role DestinationPickerDialog's own
           status line plays. -->
      <p class="sc-picker__here" aria-live="polite">{hereText}</p>
    </div>
    <div class="sc-picker__body">
      {#if listing.isPending}
        <p class="sc-picker__status">{t('common.loading')}</p>
      {:else if showError}
        <p class="sc-picker__status" role="alert">{errorText}</p>
      {:else if listing.data}
        {#if listing.data.entries.length === 0}
          <p class="sc-picker__status">{t('picker.empty')}</p>
        {:else}
          <ul class="sc-picker__entries" aria-label={atRoot ? t('picker.roots') : t('picker.here')}>
            {#each listing.data.entries as entry (entry.path)}
              <li>
                {#if entry.is_dir}
                  <button
                    type="button"
                    class="sc-picker__entry"
                    aria-label={t('picker.open_folder', { name: entry.name })}
                    onclick={() => navigate(entry.path)}
                  >
                    <Icon icon={icons.folder} size={20} />
                    <span>{entry.name}</span>
                  </button>
                {:else if mode === 'file'}
                  <button
                    type="button"
                    class="sc-picker__entry"
                    class:sc-picker__entry--selected={selected === entry.path}
                    aria-pressed={selected === entry.path}
                    onclick={() => (selected = entry.path)}
                  >
                    <Icon icon={icons.file} size={20} />
                    <span>{entry.name}</span>
                  </button>
                {:else}
                  <!-- Not a button: a folder-only picker has nothing to do
                       with a file row beyond showing it exists. -->
                  <span class="sc-picker__entry sc-picker__entry--disabled" aria-disabled="true">
                    <Icon icon={icons.file} size={20} />
                    <span>{entry.name}</span>
                  </span>
                {/if}
              </li>
            {/each}
          </ul>
        {/if}
        {#if listing.data.truncated}
          <p class="sc-picker__status" role="status">{t('picker.truncated')}</p>
        {/if}
      {/if}
    </div>
  </div>
  {#snippet actions()}
    <Button variant="text" onclick={onclose}>{t('common.cancel')}</Button>
    <Button variant="filled" disabled={!canConfirm} onclick={confirm}>{t('picker.choose')}</Button>
  {/snippet}
</Dialog>

<style>
  .sc-picker {
    display: flex;
    flex-direction: column;
    gap: 8px;
    min-width: min(420px, 80vw);
  }
  .sc-picker__nav {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .sc-picker__here {
    margin: 0;
    overflow-wrap: anywhere;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
  .sc-picker__body {
    /* Capped rather than free-growing, same reasoning as
       DestinationPickerDialog's own tree: the dialog itself would scroll
       otherwise and push the confirm button off a phone screen. */
    max-height: 40vh;
    overflow-y: auto;
    border: 1px solid var(--m3c-outline-variant);
    border-radius: var(--m3-shape-small);
  }
  .sc-picker__status {
    margin: 0;
    padding: 16px;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
  .sc-picker__entries {
    list-style: none;
    margin: 0;
    padding: 4px;
  }
  .sc-picker__entry {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    padding: 8px;
    border: none;
    border-radius: var(--m3-shape-extra-small);
    background: none;
    color: inherit;
    text-align: left;
    @apply --m3-body-medium;
  }
  button.sc-picker__entry {
    cursor: pointer;
  }
  button.sc-picker__entry:hover {
    background: var(--m3c-surface-container-highest);
  }
  .sc-picker__entry--selected {
    background: var(--m3c-secondary-container);
    color: var(--m3c-on-secondary-container);
  }
  .sc-picker__entry--disabled {
    color: var(--m3c-on-surface-variant);
    opacity: 0.6;
  }
</style>
