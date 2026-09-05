<script lang="ts">
  // Menu.svelte: Material 3 adaptive menu and mobile bottom sheet.
  // On mobile (compact viewport), renders as a Material 3 Bottom Sheet modal dialog in the
  // browser top-layer, guaranteeing full-screen width, viewport anchoring, and escape
  // from any ancestor clipping or transform contexts (including the sidebar drawer).
  // On desktop, renders as a fixed-position floating dropdown with exact pixel alignment
  // to caller coordinates, 4px grid snapping, and boundary clamping.
  import type { Snippet } from 'svelte'
  import { fade, scale } from 'svelte/transition'
  import { cubicOut } from 'svelte/easing'
  import { Menu } from 'm3-svelte'
  import { ui } from '../store/ui.store'

  interface Props {
    open: boolean
    onclose: () => void
    x?: number
    y?: number
    align?: 'start' | 'end'
    children: Snippet
  }

  let { open, onclose, x, y, align = 'start', children }: Props = $props()
  const anchored = $derived(x !== undefined && y !== undefined)
  let el: HTMLDivElement | undefined = $state()
  let sheetDialogEl: HTMLDialogElement | undefined = $state()

  // Exact desktop coordinate derivations snapped to 4px grid
  let rightOffset = $derived.by(() => {
    if (x === undefined || align !== 'end') return undefined
    const w = typeof window !== 'undefined' ? window.innerWidth : 1000
    const raw = Math.max(8, w - x)
    return Math.round(raw / 4) * 4
  })

  let leftOffset = $derived.by(() => {
    if (x === undefined || align === 'end') return undefined
    const raw = Math.max(8, x)
    return Math.round(raw / 4) * 4
  })

  let topOffset = $derived.by(() => {
    if (y === undefined) return undefined
    const raw = Math.max(8, y)
    return Math.round(raw / 4) * 4
  })

  function reduceMotion(): boolean {
    return typeof matchMedia === 'function' && matchMedia('(prefers-reduced-motion: reduce)').matches
  }

  function animDuration(ms: number): number {
    return reduceMotion() ? 0 : ms
  }

  // Mobile BottomSheet modal dialog lifecycle in browser top-layer
  $effect(() => {
    if (!open || !ui.state.compact || !sheetDialogEl) return
    const trigger = document.activeElement instanceof HTMLElement ? document.activeElement : null
    sheetDialogEl.showModal?.()
    return () => {
      sheetDialogEl?.close?.()
      trigger?.focus()
    }
  })

  function onDialogClick(e: MouseEvent): void {
    if (e.target === sheetDialogEl) {
      onclose()
    }
  }

  // Desktop outside-click listener (capture phase)
  $effect(() => {
    if (!open || ui.state.compact) return
    function onPointerDownCapture(e: PointerEvent) {
      const target = e.target as Element | null
      if (el && el.contains(target as Node)) return
      if (target?.closest('[aria-expanded="true"]')) return
      onclose()
    }
    window.addEventListener('pointerdown', onPointerDownCapture, true)
    return () => {
      window.removeEventListener('pointerdown', onPointerDownCapture, true)
    }
  })

  function onWindowKeydown(e: KeyboardEvent) {
    if (open && !ui.state.compact && e.key === 'Escape') onclose()
  }
</script>

<svelte:window onkeydown={onWindowKeydown} />

{#if open}
  {#if ui.state.compact}
    <!-- Mobile Material 3 Bottom Sheet Modal Dialog (Top-Layer) -->
    <dialog
      bind:this={sheetDialogEl}
      class="sc-sheet"
      role="menu"
      tabindex="-1"
      onclick={onDialogClick}
      onclose={() => onclose()}
      oncancel={(e) => {
        e.preventDefault()
        onclose()
      }}
    >
      <div class="sc-sheet-handle-wrap" aria-hidden="true">
        <div class="sc-sheet-handle"></div>
      </div>
      <div class="sc-sheet-content">
        {@render children()}
      </div>
    </dialog>
  {:else}
    <!-- Desktop Floating Dropdown Menu -->
    <div
      bind:this={el}
      class="shell"
      class:anchored
      style:right={rightOffset !== undefined ? `${rightOffset}px` : undefined}
      style:left={leftOffset !== undefined ? `${leftOffset}px` : (anchored && align === 'end' ? 'auto' : undefined)}
      style:top={topOffset !== undefined ? `${topOffset}px` : undefined}
      style:max-height={topOffset !== undefined ? `calc(100vh - ${topOffset}px - 8px)` : undefined}
      role="menu"
      tabindex="-1"
      in:scale={{ duration: animDuration(150), start: 0.85, opacity: 0, easing: cubicOut }}
      out:fade={{ duration: animDuration(100) }}
    >
      <Menu>{@render children()}</Menu>
    </div>
  {/if}
{/if}

<style>
  /* Desktop Dropdown Styles */
  .shell {
    position: absolute;
    z-index: 50;
    transform-origin: top right;
    max-width: calc(100vw - 16px);
    max-height: calc(100vh - 16px);
    overflow-y: auto;
  }
  .anchored {
    position: fixed;
  }
  .shell :global(.item) {
    width: 100%;
  }

  /* Mobile Bottom Sheet Styles (Top-Layer Dialog) */
  dialog.sc-sheet {
    position: fixed;
    top: auto;
    bottom: 0;
    inset-inline: 0;
    margin: 0;
    width: 100vw;
    max-width: 100vw;
    padding: 0;
    border: none;
    background: var(--m3c-surface-container-low);
    border-radius: 16px 16px 0 0;
    box-shadow: var(--m3-elevation-3);
    display: flex;
    flex-direction: column;
    overflow: hidden;
    max-height: calc(80vh - 16px);
    max-height: calc(80dvh - 16px);
    translate: 0 0;
    transition:
      translate var(--m3-duration) var(--m3-easing),
      display var(--m3-duration) allow-discrete,
      overlay var(--m3-duration) allow-discrete;
  }
  dialog.sc-sheet:not([open]) {
    display: none;
    translate: 0 100%;
  }
  @starting-style {
    dialog.sc-sheet[open] {
      translate: 0 100%;
    }
  }
  dialog.sc-sheet::backdrop {
    background: color-mix(in srgb, var(--m3c-scrim) 32%, transparent);
    transition:
      background-color var(--m3-duration) var(--m3-easing),
      display var(--m3-duration) allow-discrete,
      overlay var(--m3-duration) allow-discrete;
  }
  dialog.sc-sheet:not([open])::backdrop {
    background: color-mix(in srgb, var(--m3c-scrim) 0%, transparent);
  }
  @starting-style {
    dialog.sc-sheet[open]::backdrop {
      background: color-mix(in srgb, var(--m3c-scrim) 0%, transparent);
    }
  }

  .sc-sheet-handle-wrap {
    padding: 8px 0;
    margin-bottom: 4px;
    display: flex;
    justify-content: center;
    align-items: center;
    flex: none;
  }
  .sc-sheet-handle {
    width: 32px;
    height: 4px;
    border-radius: 9999px;
    background: var(--m3c-outline-variant);
  }
  .sc-sheet-content {
    flex: 1;
    overflow-y: auto;
    padding: 8px 12px;
    margin-bottom: env(safe-area-inset-bottom, 0px);
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .sc-sheet-content :global(button),
  .sc-sheet-content :global(.item) {
    width: 100%;
    height: 48px;
    min-height: 48px;
    border-radius: var(--m3-shape-small);
    padding: 0 16px;
    display: flex;
    align-items: center;
    gap: 16px;
    background: transparent;
    border: none;
    color: var(--m3c-on-surface);
    @apply --m3-body-large;
    font-size: 15px;
    text-align: left;
    cursor: pointer;
    transition: background var(--m3-duration-fast) var(--m3-easing);
  }
  .sc-sheet-content :global(button:hover),
  .sc-sheet-content :global(.item:hover) {
    background: var(--m3c-surface-container);
  }
  .sc-sheet-content :global(button:active),
  .sc-sheet-content :global(.item:active) {
    background: var(--m3c-surface-container-high);
  }
</style>
