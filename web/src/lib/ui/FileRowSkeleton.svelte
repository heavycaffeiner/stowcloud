<script lang="ts">
  // Placeholder for a row index whose window hasn't
  // loaded yet. task requirement #3: fast scrolling
  // must show skeletons, not blank space or a layout jump. Same height as a
  // real row so the windowing maths never has to special-case it.
  interface Props {
    rowIndex: number
  }
  let { rowIndex }: Props = $props()
</script>

<div class="sc-row-skeleton" role="row" aria-rowindex={rowIndex} aria-busy="true">
  <span class="sc-row-skeleton__cell sc-row-skeleton__cell--select" role="gridcell"></span>
  <span class="sc-row-skeleton__cell sc-row-skeleton__cell--name" role="gridcell">
    <span class="sc-row-skeleton__bar sc-row-skeleton__bar--icon"></span>
    <span class="sc-row-skeleton__bar sc-row-skeleton__bar--name"></span>
  </span>
  <span class="sc-row-skeleton__cell sc-row-skeleton__cell--size" role="gridcell">
    <span class="sc-row-skeleton__bar sc-row-skeleton__bar--size"></span>
  </span>
  <span class="sc-row-skeleton__cell sc-row-skeleton__cell--mtime" role="gridcell">
    <span class="sc-row-skeleton__bar sc-row-skeleton__bar--mtime"></span>
  </span>
</div>

<style>
  .sc-row-skeleton {
    display: flex;
    align-items: center;
    height: var(--sc-row-height);
    padding-inline: 16px;
    border-bottom: 1px solid var(--m3c-outline-variant);
  }
  .sc-row-skeleton__cell {
    display: flex;
    align-items: center;
    gap: 8px;
    overflow: hidden;
  }
  .sc-row-skeleton__cell--select {
    flex: 0 0 32px;
  }
  .sc-row-skeleton__cell--name {
    flex: 1 1 auto;
    min-width: 0;
  }
  .sc-row-skeleton__cell--size {
    flex: 0 0 112px;
    justify-content: flex-end;
  }
  .sc-row-skeleton__cell--mtime {
    flex: 0 0 176px;
    justify-content: flex-end;
  }
  .sc-row-skeleton__bar {
    display: block;
    height: 16px;
    border-radius: var(--m3-shape-extra-small);
    background: var(--m3c-surface-container-highest);
    animation: sc-row-skeleton-pulse 1.2s ease-in-out infinite;
  }
  .sc-row-skeleton__bar--icon {
    flex: 0 0 20px;
    height: 20px;
    border-radius: var(--m3-shape-small);
  }
  .sc-row-skeleton__bar--name {
    width: 60%;
  }
  .sc-row-skeleton__bar--size {
    width: 40px;
  }
  .sc-row-skeleton__bar--mtime {
    width: 88px;
  }
  /* Mirrors FileRow.svelte's compact-window column drop so the skeleton
     doesn't reserve space for a column that won't render once real rows
     arrive (that would be its own layout jump -- see the module comment). */
  @container sc-file-table (max-width: 599.98px) {
    .sc-row-skeleton__cell--mtime {
      display: none;
    }
    .sc-row-skeleton__cell--size {
      flex-basis: 88px;
    }
  }
  @keyframes sc-row-skeleton-pulse {
    0%,
    100% {
      opacity: 0.5;
    }
    50% {
      opacity: 1;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .sc-row-skeleton__bar {
      animation: none;
    }
  }
</style>
