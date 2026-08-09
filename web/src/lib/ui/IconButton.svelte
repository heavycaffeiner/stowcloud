<script lang="ts">
  // A square, icon-only m3-svelte Button, plus the label on hover.
  //
  // An icon-only control tells a screen reader what it does through
  // `aria-label` and tells everyone else nothing at all, so the label has to
  // be reachable by sight too. This used to be the native `title` attribute,
  // which never appears on keyboard focus, waits about a second, and cannot be
  // styled. The tooltip below is the same string, shown after a short hover or
  // immediately on keyboard focus.
  //
  // It is `aria-hidden`: the text is already this button's accessible name, and
  // exposing it a second time makes a screen reader read it twice.
  import type { Snippet } from 'svelte'
  import { Button } from 'm3-svelte'

  interface Props {
    label: string
    selected?: boolean
    /** Set on a button that shows and hides something else, so the state is
     *  announced as expanded/collapsed rather than as pressed. Left undefined
     *  on buttons that toggle nothing. */
    expanded?: boolean
    disabled?: boolean
    onclick?: (e: MouseEvent) => void
    children: Snippet
  }

  let { label, selected = false, expanded, disabled = false, onclick, children }: Props = $props()

  /** Long enough that dragging the pointer across a row of these does not
   *  leave a trail of tooltips behind it. */
  const HOVER_DELAY_MS = 400

  let host: HTMLElement | undefined = $state()
  let tip: HTMLElement | undefined = $state()
  let shown = $state(false)
  /** Where the button's bottom centre is. The tooltip wants to sit under it,
   *  but the last button in a row is against the edge of the window, so the
   *  final `left` is this clamped to whatever actually fits. */
  let centre = $state(0)
  let left = $state(0)
  let top = $state(0)
  let placed = $state(false)
  let timer: ReturnType<typeof setTimeout> | null = null

  const EDGE_MARGIN_PX = 8

  /** Fixed, not absolute: the selection bar and the toolbar both scroll
   *  horizontally when they run out of room, and an absolutely positioned
   *  tooltip inside one of those is clipped by it. */
  function place(): void {
    const el = host?.querySelector('button')
    if (!el) return
    const r = el.getBoundingClientRect()
    centre = Math.round(r.left + r.width / 2)
    left = centre
    top = Math.round(r.bottom + EDGE_MARGIN_PX)
  }

  $effect(() => {
    // Needs the rendered width, so it can only run once the tooltip exists.
    // Until it has, the tooltip is transparent rather than briefly off to one
    // side. Reads `centre`, writes `left`: no cycle.
    if (!shown || !tip) return
    const half = tip.offsetWidth / 2
    const lo = EDGE_MARGIN_PX + half
    const hi = window.innerWidth - EDGE_MARGIN_PX - half
    left = Math.round(hi < lo ? window.innerWidth / 2 : Math.min(Math.max(centre, lo), hi))
    placed = true
  })

  function show(delay: number): void {
    if (disabled) return
    clear()
    timer = setTimeout(() => {
      timer = null
      place()
      shown = true
    }, delay)
  }

  function clear(): void {
    if (timer) {
      clearTimeout(timer)
      timer = null
    }
  }

  function hide(): void {
    clear()
    shown = false
    placed = false
  }

  $effect(() => {
    if (!shown) return
    // The tooltip is positioned once, so anything that moves the button out
    // from under it has to dismiss it rather than drag it along.
    const onScroll = () => hide()
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') hide()
    }
    window.addEventListener('scroll', onScroll, { passive: true, capture: true })
    window.addEventListener('resize', onScroll)
    window.addEventListener('keydown', onKey)
    return () => {
      window.removeEventListener('scroll', onScroll, { capture: true })
      window.removeEventListener('resize', onScroll)
      window.removeEventListener('keydown', onKey)
    }
  })
</script>

<!-- `role="none"`: a positioning box with no semantics of its own. The pointer
     handlers only open the tooltip, and the keyboard reaches the same thing
     through focusin, so this is not a mouse-only interactive element. -->
<span
  bind:this={host}
  class="sc-icon-button"
  role="none"
  onpointerenter={() => show(HOVER_DELAY_MS)}
  onpointerleave={hide}
  onpointerdown={hide}
  onfocusin={() => show(0)}
  onfocusout={hide}
>
  <Button
    variant={selected ? 'tonal' : 'text'}
    square
    iconType="full"
    aria-label={label}
    aria-pressed={expanded === undefined ? selected : undefined}
    aria-expanded={expanded}
    {disabled}
    {onclick}
  >
    {@render children()}
  </Button>
</span>

{#if shown}
  <span
    bind:this={tip}
    class="sc-icon-button__tip"
    class:sc-icon-button__tip--placed={placed}
    aria-hidden="true"
    style:left="{left}px"
    style:top="{top}px"
  >
    {label}
  </span>
{/if}

<style>
  .sc-icon-button {
    display: inline-flex;
    /* This wrapper, not the button, is now the flex item wherever one of these
       sits in a row. Rows of icon buttons are sized to fit exactly, so it must
       not absorb the shrinking the button never did. */
    flex: none;
  }
  .sc-icon-button__tip {
    position: fixed;
    z-index: 40;
    /* `left`/`top` put the button's bottom centre here; this moves the box so
       that point is its own top centre. */
    transform: translateX(-50%);
    max-width: 240px;
    padding: 4px 8px;
    border-radius: var(--m3-shape-extra-small);
    background: var(--m3c-inverse-surface);
    color: var(--m3c-inverse-on-surface);
    @apply --m3-body-small;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    pointer-events: none;
    opacity: 0;
  }
  .sc-icon-button__tip--placed {
    opacity: 1;
  }
</style>
