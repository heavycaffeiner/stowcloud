<script lang="ts">
  // Adapter over m3-svelte's Dialog. The framework owns the surface, the
  // scrim, the top-layer transitions and `showModal()`/`close()`; this maps
  // our one-way `open` + `onclose` prop pair onto its bindable `open`, using a
  // getter/setter binding so no mirrored state can drift out of sync with the
  // parent's.
  import type { Snippet } from 'svelte'
  import { Dialog as M3Dialog } from 'm3-svelte'

  interface Props {
    open: boolean
    title: string
    onclose?: () => void
    children: Snippet
    actions?: Snippet
  }

  let { open, title, onclose, children, actions }: Props = $props()
</script>

{#snippet noActions()}{/snippet}

<M3Dialog
  bind:open={() => open, (v) => !v && onclose?.()}
  headline={title}
  buttons={actions ?? noActions}
>
  {@render children()}
</M3Dialog>
