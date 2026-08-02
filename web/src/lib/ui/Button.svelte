<script lang="ts">
  // Adapter over m3-svelte's Button. It carries no visual styling of its own:
  // variants, sizing, states, elevation and motion all come from the
  // framework. It exists for the two things the framework has no opinion
  // about.
  //
  // `loading` keeps the label in the layout (`visibility: hidden`, not
  // removed) so the button's box stays exactly its resting width — swapping
  // the label for a spinner is what used to make buttons jump width mid-click.
  //
  // `danger` remaps the colour roles rather than adding a variant, so the
  // framework's own states still apply. It is set inline because m3-svelte
  // spreads `...props` after its own `class`, so passing `class` here would
  // replace the component's styling wholesale.
  import { t } from '../i18n'
  import type { Snippet } from 'svelte'
  import { Button as M3Button, LoadingIndicator } from 'm3-svelte'

  interface Props {
    variant?: 'filled' | 'tonal' | 'outlined' | 'text' | 'elevated'
    /** MD3 error-role colouring for a destructive action (permanent purge,
     *  revoking a share link). */
    danger?: boolean
    /** MD3's reduced corner radius, for a segment inside `ConnectedButtons`
     *  — the group owns the outer rounding, the segments must not. */
    square?: boolean
    /** Toggle semantics. A filled-vs-outlined variant swap conveys the
     *  selected state to sighted users only; without this a screen reader
     *  reads three identical buttons and never says which one is on. */
    pressed?: boolean
    disabled?: boolean
    loading?: boolean
    type?: 'button' | 'submit'
    onclick?: (e: MouseEvent) => void
    children: Snippet
    icon?: Snippet
  }

  let {
    variant = 'filled',
    danger = false,
    square = false,
    pressed,
    disabled = false,
    loading = false,
    type = 'button',
    onclick,
    children,
    icon
  }: Props = $props()

  const DANGER_ROLES =
    '--m3c-primary: var(--m3c-error); --m3c-on-primary: var(--m3c-on-error); ' +
    '--m3c-secondary-container: var(--m3c-error-container); ' +
    '--m3c-on-secondary-container: var(--m3c-on-error-container); ' +
    '--m3c-outline-variant: var(--m3c-error);'
</script>

<M3Button
  {variant}
  {square}
  {type}
  {onclick}
  disabled={disabled || loading}
  iconType={icon ? 'left' : 'none'}
  style={danger ? DANGER_ROLES : undefined}
  aria-busy={loading || undefined}
  aria-pressed={pressed}
>
  {#if icon}{@render icon()}{/if}
  <span class:hidden={loading}>{@render children()}</span>
  {#if loading}
    <span class="spinner"><LoadingIndicator size={20} aria-label={t('button.working')} /></span>
  {/if}
</M3Button>

<style>
  /* Layout only. m3-svelte's `.m3-layer` already establishes the
     positioning context the spinner needs. */
  .hidden {
    visibility: hidden;
  }
  .spinner {
    position: absolute;
    inset: 0;
    display: grid;
    place-items: center;
  }
</style>
