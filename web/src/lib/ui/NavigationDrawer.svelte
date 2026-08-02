<script lang="ts">
  // NavigationDrawer.svelte — DESIGN-FRONTEND.md §3 names this in the
  // component inventory ("navigation" group) and §3 pairs it with NavigationRail
  // for the ≥905px layout, but nothing built it: the rail listed every
  // granted root directly, which is fine at two grants and wrong by
  // construction past a handful (a NavigationBar/Rail is a fixed small set
  // of destinations, not one item per grant -- see
  // `web/src/routes/(app)/+layout.svelte`).
  //
  // This is where root-switching moved to. Two presentations, exactly
  // mirroring FileTree.svelte's proven overlay/standard split (same native
  // `<dialog>` technique for free light-dismiss + focus trap + Escape, same
  // slide-from-edge motion) rather than inventing a second one:
  //  - `overlay` (compact, <905px): a modal drawer over the whole app,
  //    opened by tapping the bottom bar's "Files" item.
  //  - standard (≥905px): a plain flex sibling next to NavigationRail,
  //    open by default so switching roots still costs the one click it did
  //    before this change, not two.
  import { t } from '../i18n'
  import type { IconifyIcon } from '@iconify/types'
  import IconButton from './IconButton.svelte'
  import { Icon, NavCMLXItem } from 'm3-svelte'
  import { icons } from '../icons'

  interface DrawerItem {
    id: string
    label: string
    icon: IconifyIcon
  }
  interface Props {
    items: DrawerItem[]
    active: string
    onselect: (item: DrawerItem) => void
    /** MD3 modal vs. standard navigation drawer (DESIGN-FRONTEND.md §3) --
     *  same breakpoint signal FileTree.svelte's `overlay` prop reads. */
    overlay?: boolean
    onclose?: () => void
  }
  let { items, active, onselect, overlay = false, onclose }: Props = $props()

  let dialogEl: HTMLDialogElement | undefined = $state()

  $effect(() => {
    if (!overlay || !dialogEl) return
    // Same rationale as FileTree.svelte: `showModal()` never restores focus
    // on its own, so the trigger (the bottom bar's "Files" item) has to be
    // remembered and refocused by hand on close.
    const trigger = document.activeElement instanceof HTMLElement ? document.activeElement : null
    dialogEl.showModal()
    return () => {
      dialogEl?.close()
      trigger?.focus()
    }
  })

  function onDialogClick(e: MouseEvent): void {
    if (e.target === dialogEl) onclose?.()
  }
</script>

{#snippet list()}
  <ul class="sc-nav-drawer__list">
    {#each items as item (item.id)}
      <li>
        <!-- MD3's navigation-drawer item is exactly `NavCMLXItem`'s
             `extra-large` variant (56dp, horizontal, full-shape
             `secondary-container` pill), so the framework draws it.
             `disabled={false}` overrides its own `disabled={selected}` -- on a
             phone, re-picking the root you're already in is how this modal
             drawer gets dismissed. -->
        <NavCMLXItem
          variant="extra-large"
          icon={item.icon}
          text={item.label}
          selected={item.id === active}
          disabled={false}
          onclick={() => onselect(item)}
          aria-current={item.id === active ? 'page' : undefined}
        />
      </li>
    {/each}
  </ul>
{/snippet}

{#if overlay}
  <dialog
    bind:this={dialogEl}
    class="sc-nav-drawer sc-nav-drawer--overlay"
    aria-label={t('nav.switch_folder')}
    onclick={onDialogClick}
    onclose={() => onclose?.()}
    oncancel={() => onclose?.()}
  >
    <div class="sc-nav-drawer__overlay-header">
      <span class="sc-nav-drawer__title">{t('nav.folders')}</span>
      <IconButton label={t('common.close')} onclick={() => onclose?.()}><Icon icon={icons.close} /></IconButton>
    </div>
    {@render list()}
  </dialog>
{:else}
  <nav class="sc-nav-drawer" aria-label={t('nav.switch_folder')}>
    <div class="sc-nav-drawer__header">
      <span class="sc-nav-drawer__title">{t('nav.folders')}</span>
    </div>
    {@render list()}
  </nav>
{/if}

<style>
  .sc-nav-drawer {
    width: var(--sc-nav-drawer-width);
    overflow-y: auto;
    background: var(--m3c-surface-container-low);
    border-right: 1px solid var(--m3c-outline-variant);
    /* Was `flex: 0 0 280px; height: 100%` -- a flex-row sibling of `<main>`,
       matching NavigationRail.svelte's own (now-fixed) mistake. See that
       file's comment for the full reasoning: a plain flex sibling sits in
       normal document flow and scrolls away with a long list the instant
       the document itself became the scroller. `position: fixed`, pinned
       just right of the rail (`left`), fixes it the same way.
       The `overlay` variant below already overrides `position`/`left`/
       `height` (`.sc-nav-drawer--overlay`, later in this file, wins the
       cascade for those on the combined-class element), so this only ever
       applies to the standard (docked, ≥905px) drawer. */
    position: fixed;
    top: 0;
    left: var(--sc-nav-rail-width);
    height: 100vh;
    height: 100dvh;
    z-index: 1;
  }
  .sc-nav-drawer__header,
  .sc-nav-drawer__overlay-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    height: 56px;
    padding-inline: 16px;
  }
  .sc-nav-drawer__overlay-header {
    position: sticky;
    top: 0;
    /* `box-shadow`, not `border-bottom`: a border sits inside the border box,
       so the `height: 56px` above left 55px of content and centred the 40px
       close button on a half pixel. A shadow paints outside the box and costs
       no layout height. FileTree.svelte's overlay header does the same. */
    box-shadow: 0 1px 0 var(--m3c-outline-variant);
    background: var(--m3c-surface-container-low);
  }
  .sc-nav-drawer__title {
    @apply --m3-title-small;
    font-weight: 600;
    color: var(--m3c-on-surface-variant);
  }
  .sc-nav-drawer__list {
    list-style: none;
    margin: 0;
    /* No inline padding: `NavCMLXItem`'s extra-large variant insets its own
       pill by 20px, so adding more here would double-inset it. */
    padding: 8px 0;
    display: flex;
    flex-direction: column;
  }
  /* `NavCMLXItem` renders its label as a bare text node inside a flex box, so
     the run of text becomes an *anonymous* flex item — unreachable by any
     selector, and `text-overflow` is not inherited, so it kept its initial
     `clip` and a long root label was cut mid-glyph.
     Making `.content` a block container puts the label back in normal inline
     flow, where `text-overflow` does apply; the icon is the only other child,
     so it goes out of flow at the inline start it already sat at (16px, the
     framework's own `padding-inline`) and `line-height` takes over the
     vertical centring the flex box was doing.
     `flex: 1` while we are here: the framework shrink-wraps the pill, but
     MD3's navigation-drawer active indicator spans the track. `min-width: 0`
     because a flex item's `auto` minimum is its longest unbreakable run —
     which for `white-space: nowrap` is the whole label, i.e. exactly the
     shrink the ellipsis needs.
     Matched through `.extra-large` — the variant this drawer passes — because
     the framework's own rule for these two declarations is nested
     (`.m3-container .content`, four classes once Svelte has scoped both ends)
     and a plainer selector here loses the cascade to it. */
  .sc-nav-drawer__list :global(.m3-container.extra-large > .content) {
    display: block;
    flex: 1;
    min-width: 0;
    padding-inline-start: 52px;
    line-height: 3.5rem;
    /* The UA stylesheet centres text in a `<button>`; a shrink-wrapped flex
       item had nowhere to centre it, a full-width block does. */
    text-align: start;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  /* `NavCMLXItem` renders a `<button>`, and a button's `auto` width is
     shrink-to-fit even once `display: flex` has made it block-level — so
     without this the item grew past the 280px track instead of the label
     truncating inside it, and the drawer got a horizontal scrollbar. */
  .sc-nav-drawer__list :global(.m3-container) {
    width: 100%;
  }
  .sc-nav-drawer__list :global(.m3-container.extra-large > .content > .icon) {
    position: absolute;
    inset-inline-start: 16px;
    inset-block: 0;
  }

  .sc-nav-drawer--overlay {
    position: fixed;
    /* Stop above the bottom NavigationBar instead of running the full
       height behind it -- a `<dialog>` paints in the top layer, above
       everything, so a full-height overlay hid the bar's own content while
       still leaving its row of the screen visually claimed by both at once
       (the bar's icons showed through the undimmed strip to its right).
       Same bar-height formula NavigationBar.svelte uses for its own height,
       so the two agree on who owns the bottom of the screen without either
       hardcoding the other's size. */
    top: 0;
    bottom: calc(var(--sc-nav-bar-height) + env(safe-area-inset-bottom, 0px));
    /* `.sc-nav-drawer` (the class this shares with the standard variant)
       sets `height: 100%` for its own flex-sibling layout. Left alone here,
       that over-constrains an absolutely-positioned box that also has
       `top` and `bottom` set -- per CSS's abspos sizing rules, an explicit
       height wins and `bottom` above is silently dropped from the solved
       layout (though it still reports back from `getComputedStyle`, which
       is what made this look correct until it was actually measured). Auto
       height is what lets both insets hold at once. */
    height: auto;
    inset-inline-start: 0;
    margin: 0;
    /* MD3 modal navigation drawer: capped at 360dp, and the scrim behind it
       has to stay a usable dismiss target -- `min(320px, 85vw)` had neither
       property in any way that helped: 320px alone still measured ~83% of
       a real phone's width (leaving only a ~65px sliver of scrim), and the
       85vw term never bound anything narrower than that on a phone-sized
       viewport. 280px matches the *standard* (>=905px) drawer's own fixed
       width one component down in this file -- same panel size in both
       presentations, just modal vs. docked -- and reserving 56px of
       viewport for the scrim (MD3's usual minimum reveal for a modal
       drawer) keeps a real tap target even on the narrowest phones where
       280px alone wouldn't leave one. Comfortably inside the 360dp cap
       either way. */
    max-width: min(var(--sc-nav-drawer-width), calc(100vw - 56px));
    width: 100%;
    padding: 0;
    /* A `<dialog>` ships a UA-default border (Chrome: 3px solid, colour
       `currentColor` -- inherited page text colour, near-white in dark
       theme) that nothing here was resetting; only `border-right` was ever
       overridden for the side that touches the flush edge. The other three
       sides kept rendering that default -- the "bright 2-3px strip" the
       drawer's top edge showed above an otherwise dark UI was this, not a
       backdrop or background gap. */
    border: none;
    box-shadow: var(--m3-elevation-2);
    /* Same MD3 modal-drawer slide-in-from-edge as FileTree.svelte, same
       `@starting-style` + `allow-discrete` technique so the exit animates
       too instead of the hard cut a native `<dialog>` gives for free. */
    translate: 0 0;
    transition:
      translate var(--m3-easing),
      display var(--m3-duration) allow-discrete,
      overlay var(--m3-duration) allow-discrete;
  }
  .sc-nav-drawer--overlay:not([open]) {
    translate: -100% 0;
  }
  @starting-style {
    .sc-nav-drawer--overlay[open] {
      translate: -100% 0;
    }
  }
  .sc-nav-drawer--overlay::backdrop {
    background: color-mix(in srgb, var(--m3c-scrim) 32%, transparent);
    transition:
      background-color var(--m3-easing),
      display var(--m3-duration) allow-discrete,
      overlay var(--m3-duration) allow-discrete;
  }
  .sc-nav-drawer--overlay:not([open])::backdrop {
    background: color-mix(in srgb, var(--m3c-scrim) 0%, transparent);
  }
  @starting-style {
    .sc-nav-drawer--overlay[open]::backdrop {
      background: color-mix(in srgb, var(--m3c-scrim) 0%, transparent);
    }
  }
</style>
