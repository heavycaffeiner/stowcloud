<script lang="ts">
  import { t } from '../i18n'
  interface Crumb {
    label: string
    path: string
  }
  interface Props {
    crumbs: Crumb[]
    onnavigate: (path: string) => void
  }
  let { crumbs, onnavigate }: Props = $props()
</script>

<nav class="sc-breadcrumb" aria-label={t('breadcrumb.path')}>
  <ol class="sc-breadcrumb__list">
    {#each crumbs as c, i (c.path)}
      <li class="sc-breadcrumb__item">
        {#if i < crumbs.length - 1}
          <button
            class="sc-breadcrumb__link m3-layer sc-focus-ring"
            onclick={() => onnavigate(c.path)}>{c.label}</button
          >
          <span class="sc-breadcrumb__sep" aria-hidden="true">/</span>
        {:else}
          <span class="sc-breadcrumb__current" aria-current="page">{c.label}</span>
        {/if}
      </li>
    {/each}
  </ol>
</nav>

<style>
  .sc-breadcrumb__list {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    list-style: none;
    margin: 0;
    padding: 0;
    gap: 4px;
  }
  .sc-breadcrumb__item {
    display: inline-flex;
    align-items: center;
    gap: 4px;
  }
  .sc-breadcrumb__link {
    border: none;
    background: transparent;
    color: var(--m3c-primary);
    cursor: pointer;
    @apply --m3-title-medium;
    padding: 4px;
    border-radius: var(--m3-shape-extra-small);
  }
  .sc-breadcrumb__current {
    @apply --m3-title-medium;
    color: var(--m3c-on-surface);
    font-weight: 600;
    padding: 4px;
  }
  .sc-breadcrumb__sep {
    color: var(--m3c-on-surface-variant);
  }
</style>
