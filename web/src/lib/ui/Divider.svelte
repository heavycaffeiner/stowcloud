<script lang="ts">
  // Passthrough to m3-svelte's Divider. Kept as a file so the existing call
  // sites keep their import path; it adds nothing of its own.
  import { Divider } from 'm3-svelte'

  interface Props {
    inset?: boolean
  }
  let { inset = false }: Props = $props()
</script>

<Divider {inset} />
