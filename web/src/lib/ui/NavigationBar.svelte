<script lang="ts">
  // MD3 bottom navigation bar. The items are m3-svelte's `NavCMLXItem` in its
  // `compact` variant: that variant *is* the bottom bar's item (4rem tall,
  // vertical, animated `secondary-container` pill), so the indicator, motion,
  // state layer and type scale all come from the framework.
  //
  // The framework's matching container (`NavCMLX variant="compact"`) is
  // deliberately not used: it renders a `<nav>` and accepts no props beyond
  // `variant`/`children`, so there is no way to give the landmark an
  // accessible name. Its entire contribution is the three declarations
  // restated below; the positioning underneath them is ours either way,
  // because the framework has no opinion on where the bar sits.
  import { t } from '../i18n'
  import type { IconifyIcon } from '@iconify/types'
  import { NavCMLXItem } from 'm3-svelte'

  interface BarItem {
    id: string
    label: string
    icon: IconifyIcon
  }
  interface Props {
    items: BarItem[]
    active: string
    onselect: (id: string) => void
  }
  let { items, active, onselect }: Props = $props()
</script>

<nav class="sc-nav-bar" aria-label={t('common.main_menu')}>
  {#each items as item (item.id)}
    <!-- `disabled={false}` overrides the framework's own `disabled={selected}`
         (it spreads rest props after that attribute, so this wins). MD3
         disables the current destination because re-tapping it does nothing;
         here "Files" is not a destination but the root drawer's opener, and
         it is selected on every `/b/` page: disabled would make the drawer
         unreachable from the one screen it matters on. -->
    <NavCMLXItem
      variant="compact"
      icon={item.icon}
      text={item.label}
      selected={item.id === active}
      disabled={false}
      onclick={() => onselect(item.id)}
      aria-current={item.id === active ? 'page' : undefined}
    />
  {/each}
</nav>

<style>
  .sc-nav-bar {
    /* The declarations `NavCMLX variant="compact"` would have applied, minus
       its `justify-content: space-evenly` -- see the segment rule below. */
    display: flex;
    background: var(--m3c-surface-container);

    /* Was a plain flex sibling of `<main>` in `.sc-app-shell`'s column --
       that worked only because `.sc-app-shell__main` clipped everything
       taller than one screen (`overflow: hidden`), which is also exactly
       what kept the document from ever scrolling (see the doc comment on
       `.sc-app-shell` in +layout.svelte). Now that main's overflow is real
       (visible, not thrown away) for a long list, staying a flex sibling
       would push this bar down to the bottom of *all that overflowing
       content* instead of the bottom of the screen.
       `position: sticky; bottom: 0` looks like the fix and isn't: sticky
       pins to the bottom of its own *containing block*, and that's
       `.sc-app-shell`, which stays exactly one viewport tall on purpose
       (see its own comment -- letting it grow broke the `height: 100%`
       chain everything else here depends on). A sticky bar would "release"
       and scroll away the instant the document's real height exceeds that
       one screen, which a long list now does routinely. `position: fixed`
       has no such dependency -- it pins to the viewport itself, not to any
       ancestor's box, so it stays correct regardless of how tall the
       document under it grows. `100dvh` still matters (inherited from
       `.sc-app-shell`'s sizing, not this rule): a fixed box's `bottom: 0`
       tracks the browser's *currently visible* viewport edge as chrome
       collapses, not the largest-possible (chrome-collapsed) one `100vh`
       would measure.
       Fixed also means this bar no longer reserves its own space via flow
       -- `.sc-app-shell__main`'s `padding-bottom` (+layout.svelte) is what
       stops the last row of a list (or the last field on a settings page)
       from rendering underneath it now. */
    position: fixed;
    right: 0;
    bottom: 0;
    left: 0;
    z-index: 1;
    /* Reserve the home-indicator / gesture-bar strip below the row's own
       `--sc-nav-bar-height` of tap targets, rather than letting it eat into
       them -- the items are a fixed height (not 100% of this container) so
       this padding *adds* to the bar instead of squeezing them. `env()` is 0
       wherever there is no unsafe area (this app doesn't opt into
       `viewport-fit=cover`), so this is a no-op today and correct if that
       ever changes. NavigationDrawer.svelte's overlay variant subtracts this
       same formula from its own height so the two agree on where the bottom
       of the screen actually is. */
    padding-bottom: env(safe-area-inset-bottom, 0px);
  }

  /* MD3 divides the bar into equal segments, one per destination. The
     framework instead sizes each item to its own label and lets
     `space-evenly` share out what's left, so a row of Files/Admin/Settings
     lands its items at 26 / 147 / 269 px -- symmetric, but every edge on a
     half pixel, which is the "subtly warped" blur, and with 25px of dead
     strip at each end of a bar that is supposed to be tappable corner to
     corner.
     `flex: 1 1 0` with no cap consumes the row exactly, so there is no
     remainder to distribute and no `justify-content` to fractionalise it.
     The visible pill does not grow with the segment: it is the item's own
     `.icon`, fixed by the framework at 3.5rem x 2rem and centred. */
  .sc-nav-bar :global(> *) {
    flex: 1 1 0;
    min-width: 0;
  }
</style>
