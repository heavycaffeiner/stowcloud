<script lang="ts">
  // Adapter over m3-svelte's Chip.
  //
  // `onclick` decides the element, not just the handler. A chip with nothing
  // to click (a permission tag, a role badge, a read-only marker — most call
  // sites) must not be a `<button>`: that puts a dead tab stop and an
  // actionable-but-does-nothing announcement on every display-only chip, and a
  // grant list of 12 rows × 8 permission chips turns into 96 of them. The
  // framework's own escape hatch for this is its `label` form, which renders a
  // plain `<label>` — same box, not focusable, not announced as a control.
  //
  // `onremove` makes the whole chip the remove target rather than adding a
  // second nested button: the framework's trailing icon is decoration, and a
  // 32dp chip with a button inside a button is neither reachable by touch nor
  // valid HTML.
  import type { Snippet } from 'svelte'
  import { Chip } from 'm3-svelte'
  import { icons } from '../icons'

  interface Props {
    variant?: 'assist' | 'filter' | 'input'
    selected?: boolean
    onclick?: (e: MouseEvent) => void
    onremove?: () => void
    children: Snippet
  }

  let { variant = 'assist', selected = false, onclick, onremove, children }: Props = $props()

  // m3-svelte folds filter and suggestion into one `general` variant.
  const M3_VARIANT = { assist: 'assist', filter: 'general', input: 'input' } as const
  const action = $derived(onclick ?? (onremove ? () => onremove() : undefined))
</script>

{#if action}
  <Chip variant={M3_VARIANT[variant]} {selected} trailingIcon={onremove ? icons.close : undefined} onclick={action}>
    {@render children()}
  </Chip>
{:else}
  <Chip variant={M3_VARIANT[variant]} {selected} label>
    {@render children()}
  </Chip>
{/if}
