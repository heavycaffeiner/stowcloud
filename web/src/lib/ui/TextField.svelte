<script lang="ts">
  // Adapter over m3-svelte's TextField. The framework draws the box, the
  // floating label, the focus and error states; this adds the two things it
  // has no opinion about: the supporting-error line (MD3 specifies it,
  // m3-svelte does not ship it) and autofocus.
  import type { HTMLInputAttributes } from 'svelte/elements'
  import { TextField, TextFieldOutlined } from 'm3-svelte'

  interface Props {
    value?: string
    label?: string
    placeholder?: string
    variant?: 'filled' | 'outlined'
    error?: string | null
    /** `date` binds `YYYY-MM-DD` and draws the browser's own calendar, which
     *  is localised for free: m3-svelte ships a `DateField` instead, but its
     *  docked picker hardcodes English ("Clear/Cancel/OK", an `SMTWTFS`
     *  weekday row) with nothing to translate it through.
     *  `datetime-local` is the same control with a time beside the date, for
     *  a range bound where the day alone is too coarse to say. `number` draws
     *  the browser's own spinner and rejects non-numeric keystrokes; pair it
     *  with `min`/`max` for a declared bound so the control refuses
     *  out-of-range input before a submit ever reaches the server. */
    type?: 'text' | 'search' | 'password' | 'date' | 'datetime-local' | 'number'
    min?: number
    max?: number
    id?: string
    /** Id of a `<datalist>` to suggest from. For a field whose useful values
     *  are known but not closed: the suggestions are offered, anything else
     *  is still accepted. A `<select>` would refuse the values this build has
     *  not heard of yet. */
    list?: string
    autofocus?: boolean
    autocomplete?: HTMLInputAttributes['autocomplete']
    onkeydown?: (e: KeyboardEvent) => void
    /** Reports the field's value as the user edits it. For a caller that
     *  owns its own state rather than binding: a filter form hands each
     *  keystroke to a store that debounces it, so `bind:value` would fight
     *  the store for who holds the value. The value is passed rather than the
     *  event so no caller has to reach through `currentTarget`. */
    oninput?: (value: string) => void
  }

  let {
    value = $bindable(''),
    label,
    placeholder,
    variant = 'outlined',
    error = null,
    type = 'text',
    min,
    max,
    id,
    list,
    autofocus = false,
    autocomplete,
    onkeydown,
    oninput
  }: Props = $props()

  // The framework renders (and owns the id of) the `<input>`, so autofocus has
  // to reach it through the DOM rather than an attribute: which also avoids
  // the `autofocus` attribute's a11y warning for the callers that don't want
  // it.
  function focusInput(node: HTMLElement, enabled: boolean) {
    if (enabled) node.querySelector('input')?.focus()
  }

  // The framework floats its label on `:focus` or `:not(:placeholder-shown)`,
  // and the label carries the opaque background it needs to straddle the top
  // border. So a real placeholder pins the label *inside* the box, where that
  // background paints as a grey pill across the field: which is what the
  // download-limit and label fields of the share dialog looked like. MD3 shows
  // a placeholder only once the label is out of the way, so hand it over only
  // then; the value still shows normally, the hint waits its turn.
  let focused = $state(false)

  const Field = $derived(variant === 'filled' ? TextField : TextFieldOutlined)
</script>

<div
  class="field"
  use:focusInput={autofocus}
  onfocusin={() => (focused = true)}
  onfocusout={() => (focused = false)}
>
  <Field
    bind:value
    label={label ?? ''}
    error={!!error}
    {type}
    {min}
    {max}
    placeholder={focused ? placeholder : undefined}
    {autocomplete}
    {onkeydown}
    oninput={oninput ? (e: Event) => oninput((e.currentTarget as HTMLInputElement).value) : undefined}
    {...list ? { list } : {}}
    {...id ? { id } : {}}
  />
  {#if error}<p class="error" role="alert">{error}</p>{/if}
</div>

<style>
  .field {
    display: flex;
    flex-direction: column;
    min-width: 0;
  }
  /* m3-svelte's field box is `min-width: 15rem` (240px) and can therefore
     never shrink. Two of them next to a button is 528px, which on a 390px
     phone ran 122px off the side of the admin upload form with nothing to
     scroll it back. Every caller here sits in a column that already owns its
     own width, so the box follows the wrapper. */
  .field :global(.m3-container) {
    min-width: 0;
    width: 100%;
  }
  /* The floating label has no wrap guard, and in the raised position its top
     half sits *above* its own field; it also carries an opaque background, to
     straddle the top border. So a label long enough to wrap paints over the
     field above it, which is a translation away for any label ("New password
     (optional; empty leaves it unchanged)" wrapped to 2 lines at 390px and
     covered the field above). One line and an ellipsis keeps it inside its own
     box; the full text stays in the accessible name either way. */
  .field :global(.m3-container > label) {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    max-width: 100%;
  }
  /* MD3 supporting text. m3-svelte's field takes `error` as a boolean and
     draws no message, so the line itself is ours: built from the framework's
     own type mixin and error role, not a bespoke style. */
  .error {
    margin: 0.25rem 1rem 0;
    color: var(--m3c-error);
    @apply --m3-body-small;
  }
</style>
