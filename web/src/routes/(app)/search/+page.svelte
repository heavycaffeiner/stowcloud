<script lang="ts">
  // /search: the phone shape of the search surface.
  //
  // A page rather than a sheet, because a sheet covering a phone screen is a
  // page with a backdrop behind it. Deliberately absent from the navigation
  // bar and the drawer: search is something you start from where you are, and
  // the bar is already at the destination count Material bounds it to. The
  // search button on the browse toolbar comes here.
  import { goto } from '$app/navigation'
  import { page } from '$app/state'
  import { t } from '../../../lib/i18n'
  import { Icon } from 'm3-svelte'
  import { icons } from '../../../lib/icons'
  import IconButton from '../../../lib/ui/IconButton.svelte'
  import SearchPanel from '../../../lib/ui/SearchPanel.svelte'

  // The folder the search was started from, carried so the ranking can lift
  // that subtree the way it does on the desktop sheet.
  const scope = $derived(page.url.searchParams.get('path') ?? '')

  function back(): void {
    void goto(scope ? `/b${scope}` : '/b/')
  }
</script>

<svelte:head><title>{t('search.title')}</title></svelte:head>

<div class="sc-search-page">
  <div class="sc-search-page__inner">
    <header class="sc-search-page__header">
      <IconButton label={t('common.back')} onclick={back}><Icon icon={icons['chevron-left']} /></IconButton>
      <h1>{t('search.title')}</h1>
    </header>
    <SearchPanel {scope} autofocus />
  </div>
</div>

<style>
  .sc-search-page {
    display: flex;
    min-height: 0;
    height: 100%;
    padding: var(--sc-page-pad);
  }

  .sc-search-page__inner {
    display: flex;
    flex-direction: column;
    gap: 12px;
    width: 100%;
    min-height: 0;
  }

  .sc-search-page__header {
    display: flex;
    align-items: center;
    gap: 8px;
  }

  h1 {
    margin: 0;
    font: var(--m3-font-title-large);
  }
</style>
