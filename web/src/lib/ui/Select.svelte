<script lang="ts">
  // Adapter over m3-svelte's Select, which draws a native `<select>` with a
  // real `<label for>`. Native rather than a listbox of divs: the platform
  // control is keyboard-reachable, announces its own role and value, and on a
  // phone opens the system picker.
  //
  // It adds the same two things TextField.svelte adds for the same reasons:
  // the MD3 supporting-error line, which m3-svelte takes no boolean for here
  // and draws not at all, and a width that follows the wrapper instead of the
  // framework's fixed minimum.
  import type { ComponentProps } from 'svelte'
  import { Select } from 'm3-svelte'

  interface Props {
    value?: string
    label: string
    /** Taken from the framework's own prop rather than restated, since its
     *  option type carries every `<option>` attribute and a local copy of
     *  the two fields callers use is a different type with the same name. */
    options: ComponentProps<typeof Select>['options']
    error?: string | null
    disabled?: boolean
    id?: string
    /** Reaches the underlying `<select>`, so a test or a caller that needs to
     *  address the control by name has something stable to address. */
    testid?: string
    /** Reports the new value as the user picks one, for a caller that has
     *  work to do on a change rather than only state to hold. */
    onchange?: (value: string) => void
  }

  let {
    value = $bindable(''),
    label,
    options,
    error = null,
    disabled = false,
    id,
    testid,
    onchange
  }: Props = $props()
</script>

<div class="field">
  <Select
    bind:value
    {label}
    {options}
    {disabled}
    width="100%"
    onchange={onchange ? (e: Event) => onchange((e.currentTarget as HTMLSelectElement).value) : undefined}
    {...id ? { id } : {}}
    {...testid ? { 'data-testid': testid } : {}}
  />
  {#if error}<p class="error" role="alert">{error}</p>{/if}
</div>

<style>
  .field {
    display: flex;
    flex-direction: column;
    min-width: 0;
  }
  /* The framework's container carries its own minimum width, which cannot
     shrink inside a dialog form on a narrow screen. Every caller sits in a
     column that already owns its width. */
  .field :global(.m3-container) {
    min-width: 0;
    width: 100%;
  }
  /* MD3 supporting text, built from the framework's own type mixin and error
     role rather than a bespoke style. */
  .error {
    margin: 0.25rem 1rem 0;
    color: var(--m3c-error);
    @apply --m3-body-small;
  }
</style>
