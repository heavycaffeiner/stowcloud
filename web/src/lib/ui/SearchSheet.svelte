<script lang="ts">
  // The desktop search surface: a sheet that comes down from the top of the
  // window over whatever page you were on.
  //
  // A native `<dialog>` in the top layer, for the same reasons the drawer and
  // the folder tree use one: Escape closes it, focus is trapped inside it,
  // and it escapes every ancestor's clipping and transform context. It is not
  // a route, because on a desktop the folder underneath is the context the
  // search was started from and navigating away from it throws that away.
  import { t } from '../i18n'
  import { Icon } from 'm3-svelte'
  import { icons } from '../icons'
  import IconButton from './IconButton.svelte'
  import SearchPanel from './SearchPanel.svelte'

  interface Props {
    open: boolean
    scope?: string
    onclose: () => void
  }

  let { open, scope = '', onclose }: Props = $props()

  let dialogEl: HTMLDialogElement | undefined = $state()
  let panel: SearchPanel | undefined = $state()

  $effect(() => {
    if (!open || !dialogEl) return
    const trigger = document.activeElement instanceof HTMLElement ? document.activeElement : null
    dialogEl.showModal()
    panel?.focus()
    return () => {
      dialogEl?.close()
      trigger?.focus()
    }
  })

  function onDialogClick(e: MouseEvent): void {
    if (e.target === dialogEl) onclose()
  }
</script>

{#if open}
  <dialog
    bind:this={dialogEl}
    class="sc-search-sheet"
    aria-label={t('search.title')}
    onclick={onDialogClick}
    onclose={() => onclose()}
    oncancel={(e) => {
      e.preventDefault()
      onclose()
    }}
  >
    <div class="sc-search-sheet__body">
      <SearchPanel bind:this={panel} {scope} autofocus onnavigated={onclose}>
        {#snippet trailing()}
          <IconButton label={t('search.close')} onclick={onclose}><Icon icon={icons.close} /></IconButton>
        {/snippet}
      </SearchPanel>
    </div>
  </dialog>
{/if}

<style>
  /* Anchored to the top edge and full width: a top sheet, not a centred
     dialog. The default `<dialog>` margin would centre it. */
  .sc-search-sheet {
    inset: 0 0 auto;
    width: min(920px, calc(100% - 32px));
    max-height: 80vh;
    margin: 0 auto;
    padding: 0;
    border: none;
    border-end-start-radius: 28px;
    border-end-end-radius: 28px;
    background: var(--m3c-surface-container-high, canvas);
    color: var(--m3c-on-surface, canvastext);
    box-shadow: 0 8px 24px rgb(0 0 0 / 24%);
    overflow: hidden;
  }

  .sc-search-sheet::backdrop {
    background: rgb(0 0 0 / 32%);
  }

  .sc-search-sheet__body {
    display: flex;
    flex-direction: column;
    max-height: 80vh;
    padding: 16px;
  }

  /* Slides down from the top edge rather than fading in place: the sheet's
     own gesture is vertical, and reduced motion turns it off. */
  @media (prefers-reduced-motion: no-preference) {
    .sc-search-sheet {
      transition:
        translate 200ms ease-out,
        opacity 200ms ease-out;
      translate: 0 0;
      opacity: 1;
    }

    @starting-style {
      .sc-search-sheet {
        translate: 0 -16px;
        opacity: 0;
      }
    }
  }
</style>
