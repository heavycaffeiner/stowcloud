<script lang="ts">
  // FileTree.svelte — collapsible folder-tree side panel (
  // §3 component inventory lists `FileTree` alongside `FileTable`/`FileGrid`
  // under "app-specific"). One root node per share the session grants
  // (`/` is not a real directory), each lazily
  // expandable via FileTreeItem.
  import { t } from '../i18n'
  import { authState } from '../state/auth.svelte'
  import FileTreeItem from './FileTreeItem.svelte'
  import IconButton from './IconButton.svelte'
  import { Icon } from 'm3-svelte'
  import { icons } from '../icons'

  interface Props {
    currentPath: string
    onnavigate: (path: string) => void
    /** MD3 compact window class (<905px): a
     *  docked 200px side panel next to the file table leaves too little room
     *  for the name column to stay readable (it was measured collapsing to a
     *  handful of pixels — icon and all — at 360px). Below that breakpoint the
     *  tree renders as a modal drawer instead of a flex sibling: a native
     *  `<dialog>` gives free light-dismiss, Escape-to-close and focus
     *  trapping, the same primitive Dialog.svelte already relies on. */
    overlay?: boolean
    onclose?: () => void
  }
  let { currentPath, onnavigate, overlay = false, onclose }: Props = $props()

  const roots = $derived((authState.session?.roots ?? []).map((r) => ({ path: `/${r.label}`, name: r.label })))

  let dialogEl: HTMLDialogElement | undefined = $state()

  $effect(() => {
    if (!overlay || !dialogEl) return
    // `showModal()` moves focus inside (to the close button, since nothing
    // has `autofocus`) but never restores it — that's on the caller. Without
    // this, closing the drawer drops keyboard focus to `<body>` and a Tab
    // press starts back at the top of the page instead of picking up where
    // the user was (the toolbar's tree toggle).
    const trigger = document.activeElement instanceof HTMLElement ? document.activeElement : null
    dialogEl.showModal()
    return () => {
      dialogEl?.close()
      trigger?.focus()
    }
  })

  function onDialogClick(e: MouseEvent): void {
    // Light-dismiss: a click that lands on the `<dialog>` element itself
    // (not a descendant) is a click on the backdrop, since the drawer's own
    // content is narrower than the dialog's full-viewport backdrop box.
    if (e.target === dialogEl) onclose?.()
  }
</script>

{#snippet tree()}
  <ul role="tree" aria-label={t('tree.folder_tree')}>
    {#each roots as r (r.path)}
      <FileTreeItem path={r.path} name={r.name} depth={0} {currentPath} {onnavigate} />
    {/each}
  </ul>
{/snippet}

{#if overlay}
  <dialog
    bind:this={dialogEl}
    class="sc-file-tree sc-file-tree--overlay"
    aria-label={t('tree.folder_tree')}
    onclick={onDialogClick}
    onclose={() => onclose?.()}
    oncancel={() => onclose?.()}
  >
    <div class="sc-file-tree__overlay-header">
      <IconButton label={t('tree.close_folder_tree')} onclick={() => onclose?.()}><Icon icon={icons.close} /></IconButton>
    </div>
    {@render tree()}
  </dialog>
{:else}
  <nav class="sc-file-tree" aria-label={t('tree.folder_tree')}>
    {@render tree()}
  </nav>
{/if}

<style>
  .sc-file-tree {
    flex: 0 0 240px;
    width: 240px;
    overflow-y: auto;
    padding-block: 8px;
    border-right: 1px solid var(--m3c-outline-variant);
    background: var(--m3c-surface);
  }
  ul[role='tree'] {
    list-style: none;
    margin: 0;
    padding: 0;
  }

  /* MD3 window class "medium" (<840px): a fixed
     240px side panel is too large a bite out of a small tablet width
     alongside the file list itself. Below 905px (compact) the tree is a
     modal overlay instead (see `.sc-file-tree--overlay` below) and this
     branch never renders, so it only ever applies at 840-904px. */
  @container sc-browse-content (max-width: 839.98px) {
    .sc-file-tree {
      flex-basis: 200px;
      width: 200px;
    }
  }

  .sc-file-tree--overlay {
    position: fixed;
    top: 0;
    /* Stop above the bottom NavigationBar rather than running the full height
       behind it. Same signal (`uiState.compact`) renders both, so whenever
       this overlay exists the bar does too, and a `<dialog>` paints in the top
       layer -- a full-height panel therefore covered the bar's left 320px
       while leaving the rest of its row visible, so the bottom strip of the
       screen read as owned by two things at once. NavigationDrawer.svelte
       already stops here; the same formula keeps the two agreeing on where
       the bottom of the screen is without either hardcoding the other's size.
       `height: auto` (not the `100dvh` this had) is what lets `top` and
       `bottom` both hold: an explicit height wins over `bottom` in CSS's
       abspos sizing rules and silently drops it from the solved layout. */
    bottom: calc(var(--sc-nav-bar-height) + env(safe-area-inset-bottom, 0px));
    inset-inline-start: 0;
    margin: 0;
    max-width: min(320px, 85vw);
    width: 100%;
    height: auto;
    padding: 0;
    /* A `<dialog>` ships a UA-default border (Chrome: 3px solid `currentColor`,
       so near-white in dark theme). Only `border-right` was overridden here, so
       the other three sides kept painting it — the white outline the folder
       tree showed. Same bug NavigationDrawer.svelte already fixed. */
    border: none;
    box-shadow: var(--m3-elevation-2);
    /* MD3 modal navigation drawer motion: slides in from the edge it's
       docked to, rather than the hard cut a native `<dialog>` gives for
       free. Same `@starting-style` + `allow-discrete` technique as
       Dialog.svelte, so the exit (closing) gets the same slide, not just
       the entrance. */
    translate: 0 0;
    transition:
      translate var(--m3-easing),
      display var(--m3-duration) allow-discrete,
      overlay var(--m3-duration) allow-discrete;
  }
  .sc-file-tree--overlay:not([open]) {
    translate: -100% 0;
  }
  @starting-style {
    .sc-file-tree--overlay[open] {
      translate: -100% 0;
    }
  }
  .sc-file-tree--overlay::backdrop {
    background: color-mix(in srgb, var(--m3c-scrim) 32%, transparent);
    transition:
      background-color var(--m3-easing),
      display var(--m3-duration) allow-discrete,
      overlay var(--m3-duration) allow-discrete;
  }
  .sc-file-tree--overlay:not([open])::backdrop {
    background: color-mix(in srgb, var(--m3c-scrim) 0%, transparent);
  }
  @starting-style {
    .sc-file-tree--overlay[open]::backdrop {
      background: color-mix(in srgb, var(--m3c-scrim) 0%, transparent);
    }
  }
  .sc-file-tree__overlay-header {
    position: sticky;
    top: 0;
    display: flex;
    align-items: center;
    justify-content: flex-end;
    /* 56px total, matching NavigationDrawer.svelte's overlay header. The
       divider is a `box-shadow` hairline rather than `border-bottom` because
       a border is inside the border box: `height: 56px` + a 1px border left
       55px of content, which centred the 40px close button on a half pixel
       (and here, where the height came from padding instead, made the whole
       header 57px -- off the 4px grid and 1px taller than the drawer's). A
       shadow paints outside the box, so the layout height stays exactly 56. */
    height: 56px;
    padding-inline: 8px;
    box-shadow: 0 1px 0 var(--m3c-outline-variant);
    background: var(--m3c-surface);
  }
</style>
