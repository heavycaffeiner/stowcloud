<script lang="ts">
  // A square, icon-only m3-svelte Button. No styling of its own — this is the
  // framework's `square` + `iconType="full"` preset, kept as one component so
  // the accessible name (an icon-only control has no visible label) is
  // wired the same way everywhere.
  import type { Snippet } from 'svelte'
  import { Button } from 'm3-svelte'

  interface Props {
    label: string
    selected?: boolean
    disabled?: boolean
    onclick?: (e: MouseEvent) => void
    children: Snippet
  }

  let { label, selected = false, disabled = false, onclick, children }: Props = $props()
</script>

<Button
  variant={selected ? 'tonal' : 'text'}
  square
  iconType="full"
  aria-label={label}
  aria-pressed={selected}
  title={label}
  {disabled}
  {onclick}
>
  {@render children()}
</Button>
