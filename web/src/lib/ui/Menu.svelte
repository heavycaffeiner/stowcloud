<script lang="ts">
  // Positioning + dismissal shell around m3-svelte's Menu, which is only the
  // surface (shape, elevation, container colour) and has no opinion about
  // where it sits or when it closes.
  import type { Snippet } from 'svelte'
  import { fade, scale } from 'svelte/transition'
  import { cubicOut } from 'svelte/easing'
  import { Menu } from 'm3-svelte'

  interface Props {
    open: boolean
    onclose: () => void
    /** Viewport coordinates to anchor at, when the caller has computed a
     *  clamped position for it. The menu positions *itself* rather than being
     *  wrapped in a positioned `<div>` by the caller, because a wrapper is
     *  what broke this: callers wrapped it in `<div style="position:fixed">`,
     *  and `position: fixed` forms a stacking context, so the menu's z-index
     *  only ever competed *inside* that 0×0 wrapper. The wrapper itself was
     *  `z-index: auto`, so it sorted against `.sc-file-table` — a stacking
     *  context of its own via `contain: content` — by DOM order alone, and the
     *  file list comes later. The open menu was painted over by the rows.
     *  Keeping the position and the z-index on one element keeps one authority
     *  for where this thing sits in the stack. */
    x?: number
    y?: number
    children: Snippet
  }

  let { open, onclose, x, y, children }: Props = $props()
  const anchored = $derived(x !== undefined && y !== undefined)
  let el: HTMLDivElement | undefined = $state()

  function onWindowClick(e: MouseEvent) {
    if (open && el && !el.contains(e.target as Node)) onclose()
  }
  function onWindowKeydown(e: KeyboardEvent) {
    if (open && e.key === 'Escape') onclose()
  }

  // `{#if}` fully unmounts the menu on close, so a CSS-only transition can't
  // reach it (the exit state never renders) — Svelte's own transition
  // directives are the built-in way to animate a mount/unmount. Duration
  // collapses through `matchMedia` for prefers-reduced-motion (a JS transition
  // can't read a CSS custom property's computed *value* as a number), re-read
  // on every open so a mid-session OS setting change still takes effect.
  function reduceMotion(): boolean {
    return typeof matchMedia === 'function' && matchMedia('(prefers-reduced-motion: reduce)').matches
  }
  function menuDuration(): number {
    return reduceMotion() ? 0 : 150
  }
</script>

<svelte:window onclick={onWindowClick} onkeydown={onWindowKeydown} />

{#if open}
  <div
    bind:this={el}
    class="shell"
    class:anchored
    style:left={anchored ? `${x}px` : undefined}
    style:top={anchored ? `${y}px` : undefined}
    role="menu"
    in:scale={{ duration: menuDuration(), start: 0.85, opacity: 0, easing: cubicOut }}
    out:fade={{ duration: menuDuration() }}
  >
    <Menu>{@render children()}</Menu>
  </div>
{/if}

<style>
  /* Position and stacking only — the surface itself is m3-svelte's Menu. */
  .shell {
    position: absolute;
    z-index: 20;
    transform-origin: top left;
  }
  /* Viewport-anchored: `fixed` so the menu neither scrolls away from its
     trigger nor gets clipped by an ancestor, on the same element that carries
     the z-index. */
  .anchored {
    position: fixed;
  }
  /* A `<button>` sizes to fit-content even at `display: flex`, so a MenuItem
     only fills the menu when it is a direct flex child of m3-svelte's own
     container. The browse page nests its overflow items one `<div>` deep (for
     roving-focus keydown handling), and inside that plain block each row drew
     its hover/ripple only as wide as its own label -- 66px to 118px in the
     same 118px menu. Stated here rather than at that one call site: any
     wrapper a caller adds has the same effect. */
  .shell :global(.item) {
    width: 100%;
  }
</style>
