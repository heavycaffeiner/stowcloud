<script lang="ts">
  // Adapter over m3-svelte's Dialog. The framework owns the real modal
  // primitive, its scrim, top-layer transitions, inert background and native
  // focus containment. This wrapper adds the opener lifecycle that native
  // showModal() deliberately leaves to the caller.
  import type { Snippet } from 'svelte'
  import type { HTMLDialogAttributes } from 'svelte/elements'
  import { Dialog as M3Dialog } from 'm3-svelte'

  interface Props {
    open: boolean
    title: string
    onclose?: () => void
    children: Snippet
    actions?: Snippet
    class?: string
    role?: 'dialog' | 'alertdialog'
    closedby?: HTMLDialogAttributes['closedby']
    ariaLabel?: string
  }

  let {
    open,
    title,
    onclose,
    children,
    actions,
    class: className,
    role = 'alertdialog',
    closedby = 'any',
    ariaLabel
  }: Props = $props()

  // The framework's `open` is bindable, and it writes back to it from the
  // dialog's own toggle event. Binding a getter that returns the parent's prop
  // means that write is discarded, and the framework's internal value then
  // disagrees with ours. The mirror follows the prop and reports native close
  // requests upward.
  let shown = $state(false)
  let wasOpen = false
  let opener: HTMLElement | null = null

  function focusFallback(): void {
    // A virtualized file launcher may be gone by the time a preview closes.
    // File grids, tables, and the folder tree deliberately keep a stable
    // tabindex on their owning container, so focus can still return to the
    // current workspace instead of falling back to document.body.
    const fallback = document.querySelector<HTMLElement>(
      '[role="grid"][tabindex="0"], [role="tree"][tabindex="0"]'
    )
    fallback?.focus()
  }

  function restoreFocus(): void {
    const target = opener
    opener = null
    if (
      target?.isConnected &&
      !target.hasAttribute('disabled') &&
      !target.hasAttribute('aria-hidden')
    ) {
      target.focus()
      if (document.activeElement === target) return
    }
    focusFallback()
  }

  $effect(() => {
    if (open && !wasOpen) {
      opener = document.activeElement instanceof HTMLElement ? document.activeElement : null
      wasOpen = true
    } else if (!open && wasOpen) {
      wasOpen = false
      // Let the native dialog finish leaving the top layer before restoring
      // focus. This also handles a close initiated by a child action.
      queueMicrotask(restoreFocus)
    }
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
  class={className}
  {role}
  {closedby}
  aria-label={ariaLabel ?? title}
>
  {@render children()}
</M3Dialog>
