<script lang="ts">
  // Admin-only screen — requires `/admin/*` to "never
  // load at all" for a non-admin. This thin gate component may still load
  // (route entry itself can't be blocked), so the requirement is kept by
  // wrapping the subcomponents that touch real data in `{#await import(...)}`
  // so the fetch itself never fires — the same pattern `/settings` uses.
  //
  // API-side access control is separate and done by the server (`sc-http`'s
  // `require_admin` — hiding this screen doesn't mean the API allows it).
  // What happens here is UX only.
  //
  // The seven sections used to render as one flat scroll, which meant all
  // seven `import()`s resolved and all seven sections fetched the moment an
  // administrator opened the screen — share list, user list, group list,
  // storage estimate, upload settings, server settings and the audit log,
  // whichever one they had actually come for. They are grouped into five tabs
  // now, so only the open tab loads and fetches; the rest cost nothing until
  // they are asked for.
  //
  // Five is the ceiling, not a coincidence: m3-svelte's `Tabs` positions its
  // indicator with `nth-of-type(-n + 5)` rules, so a sixth tab would render
  // with no indicator at all. Another section means regrouping, not appending.
  //
  // `VariableTabs`, not `Tabs`: the fixed variant gives every tab an equal
  // share of the width with a 5rem floor, so five of them need 400px and a
  // 360px phone clipped "Audit log" to "Audit" against the right edge with no
  // way to reach it. The variable one sizes each tab to its label and scrolls
  // the overflow, which is MD3's own answer for a set that doesn't fit.
  import { t } from '../../../lib/i18n'
  import { replaceState } from '$app/navigation'
  import { page } from '$app/state'
  import { VariableTabs } from 'm3-svelte'
  import { authState } from '../../../lib/state/auth.svelte'
  import { syncTabHash } from '../../../lib/state/tab-hash'

  const user = $derived(authState.session?.user ?? null)
  const isAdmin = $derived(user?.is_admin ?? false)

  const tabs = [
    { name: t('common.user_2'), value: 'users' },
    { name: t('common.share'), value: 'shares' },
    { name: t('common.storage'), value: 'storage' },
    { name: t('admin.server'), value: 'server' },
    { name: t('common.audit_log'), value: 'audit' }
  ]

  const TAB_VALUES = tabs.map((t) => t.value)
  const fromHash = page.url.hash.slice(1)
  let tab = $state(TAB_VALUES.includes(fromHash) ? fromHash : 'users')

  // Same reasoning as `/settings`: the tab belongs in the URL so a reload
  // stays put, and `replaceState` keeps Back a page-level action rather than
  // a tab-level undo.
  //
  // The sync runs both ways; `syncTabHash` holds the reasoning for why the
  // hash this effect reads has to be compared against the last one it read
  // rather than the last one it wrote.
  let seen = fromHash
  $effect(() => {
    const next = syncTabHash(page.url.hash.slice(1), seen, tab, TAB_VALUES)
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
    <!-- Confirming session — the instant before layout draws the `browser`
         screen. Renders nothing. -->
  {:else if !isAdmin}
    <div class="sc-admin__inner">
      <p class="sc-admin__denied">{t('admin.only_administrators_can_see_screen')}</p>
    </div>
  {:else}
    <div class="sc-admin__head">
      <!-- Group name on the wrapper, not on `Tabs` — see the twin comment in
           `/settings` for why an `aria-label` there names every radio. -->
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
        {#await import('../../../lib/ui/admin/AuditLogSection.svelte') then mod}
          {@const AuditLogSection = mod.default}
          <section class="sc-admin__section">
            <AuditLogSection />
          </section>
        {/await}
      {/if}
    </div>
  {/if}
</div>

<style>
  /* Same restructuring as web/src/routes/(app)/settings/+page.svelte, same
   * root cause: this used to be one element carrying both the max-width
   * cap *and* `overflow-y: auto`, with `margin-inline` left at its default
   * of 0 — so the capped column sat flush against the nav rail instead of
   * centered, leaving the rest of a wide window as dead space with the
   * scrollbar rendered mid-window instead of at its edge. `.sc-admin`
   * (outer) is now just the full-width scroll owner — free width via
   * `.sc-app-shell__main`'s `align-items: stretch` — and `.sc-admin__inner`
   * is the capped, centered, padded column. Cap widened 720 -> ~960 too:
   * this page's user table benefits from the extra width same as
   * `/settings`' rows do. */
  .sc-admin {
    overflow-y: auto;
    word-break: keep-all;
  }
  .sc-admin__title,
  .sc-admin__head-inner,
  .sc-admin__inner {
    max-width: min(960px, 100%);
    margin-inline: auto;
    padding-inline: var(--sc-page-pad);
  }
  .sc-admin__title {
    padding-block: var(--sc-page-pad) 0;
  }
  /* Title scrolls, tabs pin — see the twin rule in `/settings` for why the
   * sticky and the background sit on the full-width band rather than on the
   * capped column inside it. */
  .sc-admin__head {
    position: sticky;
    top: 0;
    z-index: 2;
    background: var(--m3c-surface);
  }
  .sc-admin__head-inner {
    padding-inline: 0;
  }
  .sc-admin__inner {
    padding-block: 0 var(--sc-page-pad);
  }
  /* Same fix as web/src/routes/(app)/settings/+page.svelte: global.css zeroes
   * heading margins but never sets font-size/line-height, so an unstyled h1
   * renders at the UA default font-size (~32px) inside a line box sized by
   * the inherited body line-height (24px) — the glyphs overflow their own
   * box. This page's h1 wasn't visibly colliding with anything only because
   * the first section already carries a 32px margin-top big enough to clear
   * the overflow; it had the same underlying defect as Settings/Theme on the
   * settings page. */
  .sc-admin__title h1 {
    margin: 0 0 16px;
    @apply --m3-headline-small;
  }
  .sc-admin__section {
    margin-top: 32px;
  }
  .sc-admin__section h2 {
    margin: 0 0 8px;
    @apply --m3-title-large;
  }
  .sc-admin__denied {
    color: var(--m3c-on-surface-variant);
  }
</style>
