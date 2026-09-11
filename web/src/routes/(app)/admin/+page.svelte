<script lang="ts">
  // Admin-only screen: requires `/admin/*` to "never
  // load at all" for a non-admin. This thin gate component may still load
  // (route entry itself can't be blocked), so the requirement is kept by
  // wrapping the subcomponents that touch real data in `{#await import(...)}`
  // so the fetch itself never fires (the same pattern `/settings` uses).
  //
  // API-side access control is separate and done by the server (`sc-http`'s
  // `require_admin`: hiding this screen doesn't mean the API allows it).
  // What happens here is UX only.
  //
  // The seven sections used to render as one flat scroll, which meant all
  // seven `import()`s resolved and all seven sections fetched the moment an
  // administrator opened the screen: share list, user list, group list,
  // storage estimate, upload settings, server settings and the audit log,
  // whichever one they had actually come for. They are grouped into five tabs
  // now, so only the open tab loads and fetches; the rest cost nothing until
  // they are asked for.
  //
  // The current tab set stays bounded so every category remains direct.
  //
  // Which is what the server log did: the last tab is "Logs", and it holds
  // one section covering both logs, the audit log (who did what) and the
  // server log (what the server itself recorded), under one filter bar with
  // a graph over both. They are the two things an operator opens for the
  // same reason, and a sixth tab was not available to give the second one.
  //
  // Variable tabs keep long translated labels from being squeezed or clipped.
  import { t } from '../../../lib/i18n'
  import { replaceState } from '$app/navigation'
  import { page } from '$app/state'
  import { VariableTabs } from 'm3-svelte'
  import { icons } from '../../../lib/icons'
  import { createSession } from '../../../lib/query/session'
  import { syncTabHash } from '../../../lib/ui/tab-hash'
  const session = createSession()
  const user = $derived(session.data?.user ?? null)
  const isAdmin = $derived(user?.is_admin ?? false)

  const tabs = [
    { name: t('admin.people_and_access'), value: 'users', icon: icons.admin },
    { name: t('admin.shared_folders'), value: 'shares', icon: icons.folder },
    { name: t('admin.storage_and_transfers'), value: 'storage', icon: icons.grid },
    { name: t('admin.server_and_security'), value: 'server', icon: icons.settings },
    { name: t('common.logs'), value: 'logs', icon: icons.recent }
  ]

  const TAB_VALUES = tabs.map((t) => t.value)
  const fromHash = page.url.hash.slice(1)
  let tab = $state(TAB_VALUES.includes(fromHash) ? fromHash : 'users')

  // Same reasoning as `/settings`: the tab belongs in the URL so a reload
  // stays put, and `replaceState` keeps Back a page-level action rather than
  // a tab-level undo.
  //
  // The sync runs both ways; `syncTabHash` holds the reasoning for why the
  // hash it reads off `page.url` cannot also serve as the current address.
  let seen = fromHash
  $effect(() => {
    const next = syncTabHash(page.url.hash.slice(1), seen, location.hash.slice(1), tab, TAB_VALUES)
    seen = next.seen
    if (next.tab !== tab) tab = next.tab
    else if (next.write !== null) replaceState(`#${next.write}`, page.state)
  })
</script>

<svelte:head><title>{t('admin.admin_stowcloud')}</title></svelte:head>

<div class="sc-admin">
  <div class="sc-admin__title">
    <h1>{t('common.administrator')}</h1>
  </div>

  {#if !user}
    <!-- Confirming session: the instant before layout draws the `browser`
         screen. Renders nothing. -->
  {:else if !isAdmin}
    <div class="sc-admin__inner">
      <p class="sc-admin__denied">{t('admin.only_administrators_can_see_screen')}</p>
    </div>
  {:else}
    <div class="sc-admin__head">
      <!-- The wrapper names the group without replacing each radio label. -->
      <div class="sc-admin__head-inner" role="radiogroup" aria-label={t('admin.admin_sections')}>
        <VariableTabs bind:tab items={tabs} />
      </div>
    </div>

    <div class="sc-admin__inner">
      {#if tab === 'users'}
        {#await import('../../../lib/ui/admin/UserManagementSection.svelte') then mod}
          {@const UserManagementSection = mod.default}
          <section class="sc-admin__section">
            <h2>{t('admin.users')}</h2>
            <UserManagementSection />
          </section>
        {/await}

        {#await import('../../../lib/ui/admin/GroupManagementSection.svelte') then mod}
          {@const GroupManagementSection = mod.default}
          <section class="sc-admin__section">
            <h2>{t('admin.groups')}</h2>
            <GroupManagementSection />
          </section>
        {/await}
      {:else if tab === 'shares'}
        {#await import('../../../lib/ui/admin/ShareManagementSection.svelte') then mod}
          {@const ShareManagementSection = mod.default}
          <section class="sc-admin__section">
            <ShareManagementSection />
          </section>
        {/await}
      {:else if tab === 'storage'}
        {#await import('../../../lib/ui/admin/StorageIndexSection.svelte') then mod}
          {@const StorageIndexSection = mod.default}
          <section class="sc-admin__section">
            <StorageIndexSection />
          </section>
        {/await}

        {#await import('../../../lib/ui/admin/UploadSettingsSection.svelte') then mod}
          {@const UploadSettingsSection = mod.default}
          <section class="sc-admin__section">
            <UploadSettingsSection />
          </section>
        {/await}
      {:else if tab === 'server'}
        {#await import('../../../lib/ui/admin/ServerSettingsSection.svelte') then mod}
          {@const ServerSettingsSection = mod.default}
          <section class="sc-admin__section">
            <ServerSettingsSection />
          </section>
        {/await}
      {:else}
        {#await import('../../../lib/ui/admin/LogsSection.svelte') then mod}
          {@const LogsSection = mod.default}
          <section class="sc-admin__section">
            <LogsSection />
          </section>
        {/await}
      {/if}
    </div>
  {/if}
</div>

<style>
  /* The outer pane owns scrolling. Title, tabs, and content share one
     centered column while wide management tables keep their usable width. */
  .sc-admin {
    overflow-y: auto;
    word-break: keep-all;
  }
  .sc-admin__title,
  .sc-admin__head-inner,
  .sc-admin__inner {
    width: 100%;
    max-width: min(960px, 100%);
    box-sizing: border-box;
    margin-inline: auto;
    padding-inline: var(--sc-page-pad);
  }
  .sc-admin__title {
    padding-block: 16px 4px;
  }
  /* The title scrolls away while the tab strip stays available. */
  .sc-admin__head {
    position: sticky;
    top: 0;
    z-index: 2;
    background: var(--m3c-surface);
  }
  .sc-admin__head-inner {
    padding-inline: 0;
  }
  .sc-admin__head-inner :global(.m3-container) {
    scrollbar-width: none;
  }
  .sc-admin__head-inner :global(.m3-container::-webkit-scrollbar) {
    display: none;
  }
  @media (max-width: 599.98px) {
    .sc-admin__title {
      padding-block-start: 12px;
    }
    .sc-admin__head-inner :global(.m3-container) {
      padding-inline: 4px;
    }
    .sc-admin__head-inner :global(label) {
      padding-inline: 12px;
    }
  }
  .sc-admin__inner {
    padding-block: 12px var(--sc-page-pad);
  }
  /* Explicit heading metrics avoid inheriting the body line height. */
  .sc-admin__title h1 {
    margin: 0;
    @apply --m3-headline-small;
  }
  .sc-admin__section {
    margin-top: 24px;
  }
  .sc-admin__section h2 {
    margin: 0 0 8px;
    @apply --m3-title-large;
  }
  .sc-admin__denied {
    color: var(--m3c-on-surface-variant);
  }
</style>
