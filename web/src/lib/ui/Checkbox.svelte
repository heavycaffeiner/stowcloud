<script lang="ts">
  // Adapter over m3-svelte's Checkbox, which is a decoration drawn *around* a
  // real `<input type="checkbox">` the caller supplies and must be wrapped in
  // a `<label>` — so the input, its labelling, keyboard and AT behaviour stay
  // native. `indeterminate` is a DOM property, not an attribute, hence the
  // effect.
  import { Checkbox } from 'm3-svelte'

  interface Props {
    checked?: boolean
    indeterminate?: boolean
    label?: string
    onchange?: (checked: boolean) => void
  }

  let { checked = $bindable(false), indeterminate = false, label, onchange }: Props = $props()
  let el: HTMLInputElement | undefined = $state()

  $effect(() => {
    if (el) el.indeterminate = indeterminate
  })
</script>

<label class="row">
  <Checkbox>
    <input
      bind:this={el}
      type="checkbox"
      bind:checked
      aria-label={label}
      onchange={(e) => onchange?.((e.currentTarget as HTMLInputElement).checked)}
    />
  </Checkbox>
  {#if label}<span>{label}</span>{/if}
</label>

<style>
  /* Layout only. */
  .row {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    cursor: pointer;
  }
  .row > span {
    @apply --m3-body-medium;
  }
</style>
