<script lang="ts">
  // Adapter over m3-svelte's SelectOutlined, which draws a native `<select>`
  // with a real `<label for>`. Native rather than a listbox of divs: the
  // platform control is keyboard-reachable, announces its own role and
  // value, and on a phone opens the system picker. Outlined rather than
  // filled: every other select in the app is outlined, and the filled
  // variant's label sits inside the box, over the chosen value.
  //
  // It adds one thing the framework does not: a width that follows the
  // wrapper instead of the framework's fixed minimum.
  import type { ComponentProps } from 'svelte'
  import { SelectOutlined } from 'm3-svelte'

  interface Props {
    value?: string
    label: string
    /** Taken from the framework's own prop rather than restated, since its
     *  option type carries every `<option>` attribute and a local copy of
     *  the two fields callers use is a different type with the same name. */
    options: ComponentProps<typeof SelectOutlined>['options']
    /** Reaches the underlying `<select>`, so a test or a caller that needs to
     *  address the control by name has something stable to address. */
    testid?: string
  }

  let { value = $bindable(''), label, options, testid }: Props = $props()
</script>

<div class="field">
  <SelectOutlined bind:value {label} {options} width="100%" {...testid ? { 'data-testid': testid } : {}} />
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
</style>
