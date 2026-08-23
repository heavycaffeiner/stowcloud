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

  // The framework's `open` is bindable, and it writes back to it from the
  // dialog's own toggle event. Binding a getter that returns the parent's
  // prop means that write is discarded, and the framework's internal value
  // then disagrees with ours: it had already recorded "open", so setting the
  // prop false from the parent produced no change for its effect to act on
  // and the dialog stayed on screen with its action already carried out.
  //
  // Mirroring it and pushing the parent's value in is what keeps the two in
  // step. The mirror is not a second source of truth: it only ever follows
  // the prop, and a close the framework initiates is reported upward.
  let shown = $state(open)

  $effect(() => {
    shown = open
  })
</script>

{#snippet noActions()}{/snippet}

<M3Dialog
  bind:open={
    () => shown,
    (v) => {
      shown = v
      if (!v) onclose?.()
    }
  }
  headline={title}
  buttons={actions ?? noActions}
>
  {@render children()}
</M3Dialog>
