<script lang="ts">
  // Adapter over m3-svelte's Switch. The framework draws the track, thumb,
  // states and motion; this adds the label, which it has no opinion about.
  import { Switch } from 'm3-svelte'

  interface Props {
    checked?: boolean
    label?: string
    /** Render `label` as visible text next to the track (default). A caller
     *  placing the switch in an already-dense row can set this `false` to keep
     *  the switch's full accessible name — callers pass whole sentences, e.g.
     *  "Enable the account {name}" — without spending that sentence's width
     *  visually. See UserManagementSection.svelte: an MD3 switch's visible label is
     *  normally a word or two, and an interpolated username plus a phrase
     *  starved its own row's sibling content when rendered. */
    showLabel?: boolean
    onchange?: (checked: boolean) => void
  }

  let { checked = $bindable(false), label, showLabel = true, onchange }: Props = $props()
</script>

<label class="row">
  <Switch bind:checked aria-label={label} onchange={() => onchange?.(checked)} />
  {#if label && showLabel}<span>{label}</span>{/if}
</label>

<style>
  /* Layout only — the switch itself carries no styling of ours. */
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
