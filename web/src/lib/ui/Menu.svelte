<script lang="ts">
  // Positioning + dismissal shell around m3-svelte's Menu.
  // Uses capture-phase pointerdown listener so that clicks anywhere outside
  // the menu dismiss it reliably, while allowing trigger buttons to toggle.
  // Uses unscaled offset dimensions so positioning is exact without scale drift.
  import type { Snippet } from 'svelte'
  import { fade, scale } from 'svelte/transition'
  import { cubicOut } from 'svelte/easing'
  import { Menu } from 'm3-svelte'

  interface Props {
    open: boolean
    onclose: () => void
    x?: number
    y?: number
    children: Snippet
  }

  let { open, onclose, x, y, children }: Props = $props()
  const anchored = $derived(x !== undefined && y !== undefined)
  let el: HTMLDivElement | undefined = $state()

  let posX = $state(0)
  let posY = $state(0)

  // Clamp position to viewport bounds using unscaled dimensions
  $effect(() => {
    if (!open || x === undefined || y === undefined) return
    const pad = 8
    const width = el?.offsetWidth ?? 180
    const height = el?.offsetHeight ?? 200
    const maxRight = window.innerWidth - pad
    const maxBottom = window.innerHeight - pad

    let nextX = x
    let nextY = y

    if (nextX + width > maxRight) {
      nextX = Math.max(pad, maxRight - width)
    }
    if (nextY + height > maxBottom) {
      nextY = Math.max(pad, maxBottom - height)
    }

    posX = Math.round(nextX)
    posY = Math.round(nextY)
  })

  // Capture-phase listener guarantees outside clicks close the menu even
  // when descendants or sibling elements intercept or stop propagation.
  $effect(() => {
    if (!open) return
    function onPointerDownCapture(e: PointerEvent) {
      const target = e.target as Element | null
      if (el && el.contains(target as Node)) return

      // If clicking the trigger that owns this open menu, let its click handler toggle it closed.
      if (target?.closest('[aria-expanded="true"]')) {
        return
      }

      onclose()
    }
    window.addEventListener('pointerdown', onPointerDownCapture, true)
    return () => {
      window.removeEventListener('pointerdown', onPointerDownCapture, true)
    }
  })

  function onWindowKeydown(e: KeyboardEvent) {
    if (open && e.key === 'Escape') onclose()
  }

  function reduceMotion(): boolean {
    return typeof matchMedia === 'function' && matchMedia('(prefers-reduced-motion: reduce)').matches
  }
  function menuDuration(): number {
    return reduceMotion() ? 0 : 150
  }
</script>

<svelte:window onkeydown={onWindowKeydown} />

{#if open}
  <div
    bind:this={el}
    class="shell"
    class:anchored
    style:left={anchored ? `${posX}px` : undefined}
    style:top={anchored ? `${posY}px` : undefined}
    role="menu"
    in:scale={{ duration: menuDuration(), start: 0.85, opacity: 0, easing: cubicOut }}
    out:fade={{ duration: menuDuration() }}
  >
    <Menu>{@render children()}</Menu>
  </div>
{/if}

<style>
  .shell {
    position: absolute;
    z-index: 20;
    transform-origin: top left;
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
</style>
