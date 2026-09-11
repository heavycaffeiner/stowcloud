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
    <!-- Repeat taps stay active. Files returns to the last valid workspace;
         Menu opens the remaining destinations. -->
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
    display: flex;
    justify-content: center;
    background: var(--m3c-surface-container);
    border-top: 1px solid var(--m3c-outline-variant);
    box-shadow: 0 -4px 16px color-mix(in srgb, var(--m3c-scrim) 8%, transparent);

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

  .sc-nav-bar :global(> *) {
    flex: 0 1 6.5rem;
    min-width: 0;
    max-width: 7rem;
  }
</style>
