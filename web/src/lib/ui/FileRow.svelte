<script lang="ts">
  import type { Entry } from '../api/client'
  import { formatBytes } from '../format/bytes'
  import { formatDateNs, t } from '../i18n'
  import { Checkbox, Icon } from 'm3-svelte'
  import { icons } from '../icons'

  interface Props {
    entry: Entry
    rowIndex: number
    selected: boolean
    focused: boolean
    domId: string
    onclick: (e: MouseEvent) => void
    ondblclick: () => void
    oncontextmenu: (e: MouseEvent) => void
    /** Checkbox tap/click — always toggles this row into/out of the
     *  selection, independent of shift/ctrl. See the checkbox's own comment
     *  below for why this exists as a separate handler from `onclick`. */
    ontogglecheck: () => void
  }

  let { entry, rowIndex, selected, focused, domId, onclick, ondblclick, oncontextmenu, ontogglecheck }: Props = $props()
  const iconName = $derived(entry.kind === 'dir' ? 'folder' : entry.preview?.available ? 'image' : 'file')

  // Rows are intentionally NOT individually focusable — FileTable.svelte owns
  // the single tab stop and roving "virtual focus" via aria-activedescendant
  // (see the comment at the top of FileTable.svelte). Per-row tabindex would
  // give a 100k-row directory 100k tab stops, which
  // calls out explicitly as broken accessibility. The row's click/dblclick/
  // contextmenu handlers are deliberately mouse-only for the same reason;
  // FileTable.svelte's onkeydown covers the equivalent keyboard actions
  // (Space toggles the focused row, Shift+Arrow extends a range).

  function onCheckboxClick(e: MouseEvent): void {
    // Without this, the click bubbles to the row's own `onclick` (see the
    // markup below), which runs `selectOnly` for a plain click — so tapping
    // the checkbox looked like "add to selection" but actually replaced it.
    // Verified live: two taps on two different rows' checkboxes on a real
    // touch emulation left exactly one row selected, not two. Desktop
    // multi-select still has shift/ctrl+click; touch has no modifier keys at
    // all, so the checkbox has to be a real, independent toggle target — the
    // one touch-reachable way to build a multi-selection.
    e.stopPropagation()
    ontogglecheck()
  }
</script>

<!-- svelte-ignore a11y_click_events_have_key_events, a11y_interactive_supports_focus -->
<div
  id={domId}
  class="sc-row m3-layer"
  class:sc-row--selected={selected}
  class:sc-row--focused={focused}
  role="row"
  aria-rowindex={rowIndex}
  aria-selected={selected}
  {onclick}
  {ondblclick}
  {oncontextmenu}
>
  <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
  <span
    class="sc-row__cell sc-row__cell--select sc-touch-target"
    role="gridcell"
    onclick={onCheckboxClick}
  >
    <!-- m3-svelte's Checkbox is normally wrapped in a `<label>` so the label
         forwards the click to the visually hidden input. Not here: the cell
         above owns the click (it toggles the row's selection, not the input's
         own state) and the input is deliberately inert — `tabindex="-1"`
         because the grid has one tab stop, no-op `onchange` because selection
         lives in `browse`. The framework's visuals key off the input's
         `:checked`/`:disabled` state via sibling selectors, so they still
         work without the label. -->
    <Checkbox>
      <input
        type="checkbox"
        id={`${domId}-check`}
        name={`${domId}-check`}
        checked={selected}
        tabindex="-1"
        aria-label={t('common.select', { name: entry.name })}
        onchange={() => {}}
      />
    </Checkbox>
  </span>
  <span class="sc-row__cell sc-row__cell--name" role="gridcell">
    <!-- Explicit 20: m3-svelte's Icon falls back to `1em`, which inside a row
         set in body-medium is 14px. Every other icon here is sized in px. -->
    <Icon icon={icons[iconName]} size={20} />
    <span class="sc-filename">{entry.name}</span>
    {#if entry.confusable}
      <span class="sc-row__badge" title={t('common.look_alike_characters')}><Icon icon={icons.warning} size={14} /></span>
    {/if}
  </span>
  <span class="sc-row__cell sc-row__cell--size" role="gridcell">
    {entry.kind === 'dir' ? '—' : formatBytes(entry.size)}
  </span>
  <span class="sc-row__cell sc-row__cell--mtime" role="gridcell">{formatDateNs(entry.mtime_ns)}</span>
</div>

<style>
  .sc-row {
    display: flex;
    align-items: center;
    height: var(--sc-row-height);
    padding-inline: 16px;
    cursor: pointer;
    border-bottom: 1px solid var(--m3c-outline-variant);
    color: var(--m3c-on-surface);
  }
  .sc-row--selected {
    background: var(--m3c-secondary-container);
    color: var(--m3c-on-secondary-container);
  }
  .sc-row--focused {
    outline: 3px solid var(--m3c-secondary);
    outline-offset: -3px;
  }
  .sc-row__cell {
    display: flex;
    align-items: center;
    gap: 8px;
    overflow: hidden;
  }
  .sc-row__cell--select {
    flex: 0 0 32px;
    /* Overrides the base `.sc-row__cell` rule's `overflow: hidden` (there for
       the name cell's ellipsis, not needed here — a checkbox never overflows
       its own cell) so `.sc-touch-target`'s enlarged hit area isn't clipped
       back down to the visible 32px box it's meant to expand past. */
    overflow: visible;
  }
  .sc-row__cell--name {
    flex: 1 1 auto;
    min-width: 0;
  }
  .sc-row__cell--size {
    flex: 0 0 112px;
    justify-content: flex-end;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
  .sc-row__cell--mtime {
    flex: 0 0 176px;
    justify-content: flex-end;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
  .sc-row__badge {
    display: inline-flex;
    color: var(--m3c-error);
  }

  /* MD3 window class "compact" (<600px,): the
     select + size + mtime columns alone already add up to more than a phone
     screen, which otherwise squeezes the name column -- the one thing a
     file browser can't afford to truncate -- down to a handful of pixels.
     Drop the least essential column and let size shrink instead of hiding
     any of it. Queried against FileTable's own box (see FileTable.svelte),
     not the viewport, per §3. */
  @container sc-file-table (max-width: 599.98px) {
    .sc-row__cell--mtime {
      display: none;
    }
    .sc-row__cell--size {
      flex-basis: 88px;
    }
  }
</style>
