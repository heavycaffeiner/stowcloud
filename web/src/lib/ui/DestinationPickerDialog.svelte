<script lang="ts">
  // DestinationPickerDialog — picks the folder a selection is moved or copied
  // into. Both operations share one dialog because they share the whole
  // interaction: the only difference is whether the source survives, which is
  // a choice better made *after* seeing where the files are going than before
  // opening two near-identical pickers from two menu entries.
  //
  // The folder list is `FileTreeItem`, the same lazily-expanding node the
  // browse sidebar uses — a picker with its own tree implementation would be a
  // second thing to keep agreeing with `DESIGN-CORE.md` §3.5's "/ is not a
  // directory" projection.
  import { api, type MovePreflight } from '../api/client'
  import { destinationProblem, type DestinationProblem } from '../api/path-utils'
  import { formatBytes } from '../format/bytes'
  import { t } from '../i18n'
  import { authState } from '../state/auth.svelte'
  import Button from './Button.svelte'
  import Dialog from './Dialog.svelte'
  import FileTreeItem from './FileTreeItem.svelte'

  interface Props {
    open: boolean
    /** Full virtual paths of what is being sent, not names. */
    sources: string[]
    onclose: () => void
    onpick: (dest: string, mode: 'move' | 'copy') => void
  }
  let { open, sources, onclose, onpick }: Props = $props()

  const roots = $derived((authState.session?.roots ?? []).map((r) => ({ path: `/${r.label}`, name: r.label })))

  let selected = $state<string | null>(null)
  let preflight = $state<MovePreflight | null>(null)

  const problem = $derived<DestinationProblem | null>(selected ? destinationProblem(selected, sources) : null)
  // A copy into the source's own folder is the ordinary duplicate case, so
  // only `into_itself` disqualifies it; a move there would be a no-op.
  const canMove = $derived(selected !== null && problem === null)
  const canCopy = $derived(selected !== null && problem !== 'into_itself')

  $effect(() => {
    if (!open) {
      selected = null
      preflight = null
    }
  })

  // Asks the server whether this move is a rename or a cross-device
  // copy-then-delete *before* the user commits — see `MovePreflight`. Each
  // answer is claimed by the selection that asked for it, so a slow reply for
  // a folder the user has already moved on from cannot overwrite a newer one.
  $effect(() => {
    const dest = selected
    preflight = null
    if (!dest || !canMove) return
    let live = true
    api
      .movePreflight({ paths: sources, dest, on_conflict: 'Fail' })
      .then((p) => {
        if (live) preflight = p
      })
      .catch(() => {
        // The notice is an extra courtesy; a server that will not answer it
        // still answers the move itself, and its errors belong there.
      })
    return () => {
      live = false
    }
  })
</script>

<Dialog {open} title={t('dest.move_or_copy')} {onclose}>
  <div class="sc-dest">
    <p class="sc-dest__prompt">{t('dest.choose_destination_folder', { count: sources.length })}</p>
    <div class="sc-dest__tree">
      <ul role="tree" aria-label={t('dest.destination_folder')}>
        {#each roots as r (r.path)}
          <FileTreeItem
            path={r.path}
            name={r.name}
            depth={0}
            currentPath={selected ?? ''}
            onnavigate={(p) => (selected = p)}
          />
        {/each}
      </ul>
    </div>
    <!-- `aria-live`: the selection, the reason a button is disabled and the
         cross-device warning all appear here without moving focus, so a screen
         reader user gets nothing at all unless the region announces itself. -->
    <p class="sc-dest__status" class:sc-dest__status--warn={problem !== null} aria-live="polite">
      {#if problem === 'into_itself'}
        {t('dest.cannot_move_folder_into_itself')}
      {:else if problem === 'same_folder'}
        {t('dest.already_in_this_folder')}
      {:else if selected}
        {selected}
      {:else}
        {t('dest.no_folder_chosen')}
      {/if}
    </p>
    {#if preflight?.will_copy}
      <p class="sc-dest__status sc-dest__status--notice">
        {t('dest.cross_device_move_rewrites_bytes', { size: formatBytes(preflight.total_bytes) })}
      </p>
    {/if}
  </div>
  {#snippet actions()}
    <Button variant="text" onclick={onclose}>{t('common.cancel')}</Button>
    <Button variant="outlined" disabled={!canCopy} onclick={() => selected && onpick(selected, 'copy')}>
      {t('common.copy')}
    </Button>
    <Button variant="filled" disabled={!canMove} onclick={() => selected && onpick(selected, 'move')}>
      {t('common.move')}
    </Button>
  {/snippet}
</Dialog>

<style>
  .sc-dest {
    /* The dialog is as wide as m3-svelte lets it be on a phone; the tree needs
       every pixel of that for a deep path to stay readable. */
    min-width: min(360px, 72vw);
  }
  .sc-dest__prompt {
    margin: 0 0 8px;
    @apply --m3-body-medium;
  }
  .sc-dest__tree {
    /* Capped rather than free-growing: the dialog itself is what scrolls
       otherwise, which pushes the confirm buttons off a phone screen once a
       few folders are expanded. */
    max-height: 40vh;
    overflow-y: auto;
    padding-block: 4px;
    border: 1px solid var(--m3c-outline-variant);
    border-radius: var(--m3-shape-small);
  }
  .sc-dest__tree ul[role='tree'] {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .sc-dest__status {
    /* Reserves its line so the tree does not jump up and down as the message
       under it appears and disappears. */
    min-height: 20px;
    margin: 8px 0 0;
    color: var(--m3c-on-surface-variant);
    overflow-wrap: anywhere;
    @apply --m3-body-small;
  }
  .sc-dest__status--warn {
    color: var(--m3c-error);
  }
  /* The cross-device notice is a caution, not a refusal — the move is still
     allowed, it will just take real time. Error red would read as "this
     failed" for something the user is about to be allowed to do. */
  .sc-dest__status--notice {
    color: var(--m3c-tertiary);
  }
</style>
