<script lang="ts">
  // Not an adapter over m3-svelte's ListItem: that one takes `headline` and
  // `supporting` as plain strings, and every call site here puts markup in
  // them (chips, buttons, a 37-line block in GrantManagementSection). So this
  // stays an app composition, but built out of the framework's own state
  // layer, focus ring, colour roles and type mixins rather than its own.
  import type { Snippet } from 'svelte'

  interface Props {
    selected?: boolean
    onclick?: (e: MouseEvent) => void
    leading?: Snippet
    trailing?: Snippet
    headline: Snippet
    supporting?: Snippet
  }

  let { selected = false, onclick, leading, trailing, headline, supporting }: Props = $props()
</script>

<!-- The state layer is conditional: `m3-layer` tints on hover and on press,
     and CSS hover reaches an ancestor, so a row carrying one lit up whenever
     a button inside it was hovered or tapped. The row and the control both
     tinted, one over the other, and moving between a row's controls kept
     re-lighting the pair. A row that cannot be pressed has no state to show. -->
<div
  class="sc-list-item sc-focus-ring"
  class:m3-layer={onclick !== undefined}
  class:sc-list-item--selected={selected}
  {onclick}
  role="presentation"
>
  {#if leading}<span class="sc-list-item__leading">{@render leading()}</span>{/if}
  <span class="sc-list-item__text">
    <span class="sc-list-item__headline">{@render headline()}</span>
    {#if supporting}<span class="sc-list-item__supporting">{@render supporting()}</span>{/if}
  </span>
  {#if trailing}<span class="sc-list-item__trailing">{@render trailing()}</span>{/if}
</div>

<style>
  /* `flex-wrap` + a floor on the text column, together, are what keep a row
   * with a busy trailing group readable on a phone. `__trailing` is
   * `flex-shrink: 0` (it holds switches and 40px icon buttons, none of which
   * can give up width), so on a 360px screen the admin user row's four
   * controls took ~280px of the 328px available and the name column was
   * squeezed to a ~50px sliver: the user's name rendered on top of the
   * Admin chip and the switch. Giving the text a 12rem basis it cannot shrink below
   * means that, when the two groups no longer fit side by side, the trailing
   * group wraps onto its own line instead of eating the name. */
  .sc-list-item {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 16px;
    min-height: var(--sc-row-height);
    padding-block: 8px;
    padding-inline: 16px;
    cursor: pointer;
    color: var(--m3c-on-surface);
  }
  .sc-list-item--selected {
    background: var(--m3c-secondary-container);
    color: var(--m3c-on-secondary-container);
  }
  .sc-list-item__leading {
    display: inline-flex;
    /* An icon is 20px and this slot is 32px, so without these it sat in the
       top-left corner with 12px of empty box below and to the right of it, and
       every leading icon in every list read 6px high of where its row was. */
    align-items: center;
    justify-content: center;
    flex-shrink: 0;
    width: 32px;
    height: 32px;
  }
  .sc-list-item__text {
    display: flex;
    flex-direction: column;
    min-width: 0;
    flex: 1 0 12rem;
  }
  /* A headline is a row, not a line of text: nearly every caller puts a chip
   * or a badge beside the name. A chip is a flex box, so inside an inline
   * headline it became block-level and spanned the full row width: four
   * callers had each rediscovered that and pasted this same override back. */
  .sc-list-item__headline {
    display: flex;
    align-items: center;
    min-width: 0;
    gap: 8px;
    @apply --m3-body-large;
  }
  .sc-list-item__supporting {
    @apply --m3-body-small;
    color: var(--m3c-on-surface-variant);
  }
  .sc-list-item__trailing {
    display: inline-flex;
    flex-wrap: wrap;
    align-items: center;
    /* The group hangs off the right edge, so a control pushed onto a second
       line belongs under the others rather than alone at the far left. */
    justify-content: flex-end;
    gap: 8px;
    margin-left: auto;
    max-width: 100%;
  }
</style>
