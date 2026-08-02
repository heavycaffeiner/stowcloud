<script lang="ts">
  // MD3 navigation rail, wrapping m3-svelte's own `NavigationRail` /
  // `NavigationRailItem`. The framework renders a plain `<div>`, so the
  // landmark and its accessible name are ours to supply; everything visual
  // (96px track, 56x32 pill indicator, label, state layer, motion) is the
  // framework's.
  //
  // `collapse="no"`: the rail has 2-3 fixed destinations and the root list
  // lives in NavigationDrawer, so there is nothing an expand toggle would
  // reveal. It also keeps the width a constant the shell can reserve.
  import { t } from '../i18n'
  import type { IconifyIcon } from '@iconify/types'
  import { NavigationRail as M3NavigationRail, NavigationRailItem } from 'm3-svelte'

  interface RailItem {
    id: string
    label: string
    icon: IconifyIcon
  }
  interface Props {
    items: RailItem[]
    active: string
    onselect: (id: string) => void
  }
  let { items, active, onselect }: Props = $props()
</script>

<nav class="sc-nav-rail" aria-label={t('common.main_menu')}>
  <M3NavigationRail collapse="no">
    {#each items as item (item.id)}
      <NavigationRailItem
        label={item.label}
        icon={item.icon}
        active={item.id === active}
        onclick={() => onselect(item.id)}
        aria-current={item.id === active ? 'page' : undefined}
      />
    {/each}
  </M3NavigationRail>
</nav>

<style>
  .sc-nav-rail {
    /* Was `height: 100%` on a flex-row sibling of `<main>` in
       `.sc-app-shell`, stretched to match its (fixed, one-viewport-tall)
       height. That's still a correct *size*, but it was never a correct
       *position*: a plain flex sibling sits in normal document flow, and
       once `.sc-app-shell__main` stopped clipping (+layout.svelte) and the
       document became the real scroller, this rail scrolled away with
       everything else the instant the page moved -- there was just never
       any scrolling to reveal that before. `position: fixed` pins it to
       the viewport itself regardless of document scroll, the same fix
       `NavigationBar.svelte`'s own comment describes for the bottom bar
       (see that comment for why `sticky` doesn't work: its containing
       block, `.sc-app-shell`, stays exactly one viewport tall on purpose,
       which isn't enough room for `sticky` to hold through a long scroll).
       Fixed also takes it out of flow, so `.sc-app-shell__main`'s own
       `padding-left` (+layout.svelte, `--rail` modifier) is what reserves
       `--sc-nav-rail-width` now instead of flex distribution doing it for
       free. The explicit width matters for the same reason: a fixed box
       shrink-wraps its content, and the framework's own track is only as
       wide as this because both read the same variable. */
    position: fixed;
    top: 0;
    left: 0;
    width: var(--sc-nav-rail-width);
    height: 100vh;
    height: 100dvh;
    z-index: 1;
  }

  /* The framework gives the label a 56px box and leaves it overflowing, so a
     word with no break opportunity just keeps going: "Administrator" wants 83px
     and ran out of the rail into the drawer behind it. The labels are short
     enough not to hit this (see `nav.admin`), but a label is translated text
     and the next one might not be. */
  .sc-nav-rail :global(span:last-of-type) {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
</style>
