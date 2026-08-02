<script lang="ts">
  // Shim onto m3-svelte's snackbar, which is a global singleton driven by a
  // `snackbar(...)` call plus one `<Snackbar />` host mounted in
  // `(app)/+layout.svelte` — not a per-message component. Pages here still own a
  // `snackbarMsg` string, so this forwards it and hands the lifetime
  // (timeout, dismissal, stacking) to the framework, which is why `ondismiss`
  // fires immediately: from that point the message is no longer ours.
  import { untrack } from 'svelte'
  import { snackbar } from 'm3-svelte'

  interface Props {
    message: string | null
    actionLabel?: string
    onaction?: () => void
    ondismiss?: () => void
  }

  let { message, actionLabel, onaction, ondismiss }: Props = $props()

  $effect(() => {
    const text = message
    if (!text) return
    untrack(() => {
      snackbar(text, actionLabel && onaction ? { [actionLabel]: onaction } : undefined)
      ondismiss?.()
    })
  })
</script>
