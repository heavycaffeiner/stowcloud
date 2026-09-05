<script lang="ts">
  // Adapter over m3-svelte's Switch. The framework draws the track, thumb,
  // states and motion; this adds the label, which it has no opinion about.
  import { Switch } from 'm3-svelte'

  interface Props {
    checked?: boolean
    disabled?: boolean
    label?: string
    /** Render `label` as visible text next to the track (default). A caller
     *  placing the switch in an already-dense row can set this `false` to keep
     *  the switch's full accessible name (callers pass whole sentences, e.g.
     *  "Enable the account {name}") without spending that sentence's width
     *  visually. See UserManagementSection.svelte: an MD3 switch's visible label is
     *  normally a word or two, and an interpolated username plus a phrase
     *  starved its own row's sibling content when rendered. */
    showLabel?: boolean
    onchange?: (checked: boolean) => void
  }

  let { checked = false, disabled = false, label, showLabel = true, onchange }: Props = $props()

  // What the track draws, seeded from the caller so a switch that mounts on
  // does not paint one frame off first. It follows `checked` on every change,
  // so a refused or cancelled write puts the control back as soon as the
  // caller's state comes back unchanged.
  //
  // svelte-ignore state_referenced_locally
  let shown = $state(checked)
  $effect(() => {
    shown = checked
  })

  // A click proposes a change; it does not make one. The control is put back
  // immediately and moves only when the parent says the value moved, so a
  // dismissed confirm dialog and a server refusal both leave it telling the
  // truth rather than the click's optimism.
  function propose(): void {
    const wanted = shown
    shown = checked
    onchange?.(wanted)
  }
</script>

<label class="row" class:disabled>
  <Switch bind:checked={shown} {disabled} aria-label={label} onchange={propose} />
  {#if label && showLabel}<span>{label}</span>{/if}
</label>

<style>
  /* Layout only: the switch itself carries no styling of ours. */
  .row {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    cursor: pointer;
  }
  /* A switch the caller refuses to move: the pointer must not promise a
     click that is guaranteed to be rejected. */
  .row.disabled {
    cursor: not-allowed;
    opacity: 0.38;
  }
  .row > span {
    @apply --m3-body-medium;
  }
</style>
