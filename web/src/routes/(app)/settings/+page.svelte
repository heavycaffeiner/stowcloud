<script lang="ts">
  // Personal settings. Administrator-only screens live at `/admin`, which is
  // its own nav destination.
  //
  // The eight sections are split across tabs rather than one flat scroll, for
  // two reasons:
  //  - Three of them fetch on mount (`AppPasswordsSection` and
  //    `SessionsSection` in `onMount`, `TotpSection`'s recovery-code count in
  //    an `$effect`), so opening this page to change the theme would fire
  //    three API calls nobody asked for. Each tab's sections are dynamically
  //    imported, so an unopened tab neither loads nor fetches.
  //  - On a phone one long unlandmarked scroll is hard to navigate.
  import { currentLocale, setLocale, t } from '../../../lib/i18n'
  import { goto, replaceState } from '$app/navigation'
  import { page } from '$app/state'
  import { ConnectedButtons, Tabs } from 'm3-svelte'
  import { api } from '../../../lib/api/client'
  import { fetchOidcConfig } from '../../../lib/api/oidc'
  import type { OidcConfig } from '../../../lib/api/types'
  import { authState, setAnonymous } from '../../../lib/state/auth.svelte'
  import Button from '../../../lib/ui/Button.svelte'
  import Divider from '../../../lib/ui/Divider.svelte'
  import { setTheme, uiState } from '../../../lib/state/ui.svelte'
  import { syncTabHash } from '../../../lib/state/tab-hash'

  let loggingOut = $state(false)

  const user = $derived(authState.session?.user ?? null)
  const features = $derived(authState.session?.features ?? null)
  const oidc = $derived(authState.session?.oidc ?? null)

  // Single sign-on (`docs/proposals/stowcloud-0-oidc-login.md` §4.3.2). Asked
  // here rather than inside the section, because this page owns the heading
  // and the dividers around it: a component that renders nothing would still
  // leave "Single sign-on" with a paragraph under it and no control, on every
  // deployment that has no identity provider.
  //
  // Three reasons to show it, and the third is the one that is easy to miss: a
  // link flow that failed comes back to this screen with `?oidc_error=`, and
  // that message has nowhere to land if the section is not rendered.
  let ssoConfig = $state<OidcConfig | null>(null)
  fetchOidcConfig().then((c) => (ssoConfig = c))
  const ssoVisible = $derived(
    ssoConfig !== null && (ssoConfig.enabled || (oidc?.linked ?? false) || page.url.searchParams.has('oidc_error'))
  )

  const tabs = $derived([
    { name: t('settings.account'), value: 'account' },
    { name: t('settings.security'), value: 'security' },
    ...(features?.smb ? [{ name: t('settings.connections'), value: 'connections' }] : []),
    { name: t('settings.appearance'), value: 'appearance' }
  ])

  const TAB_VALUES = ['account', 'security', 'connections', 'appearance']
  const fromHash = page.url.hash.slice(1)
  let tab = $state(TAB_VALUES.includes(fromHash) ? fromHash : 'account')

  // The tab lives in the URL so a reload, or a link someone pastes to a
  // colleague, lands on the same one. `replaceState` rather than `pushState`:
  // switching tabs is not a navigation a Back press should have to undo one
  // step at a time.
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

  // `TAB_VALUES` lists every tab that can exist, not every tab this user has —
  // it has to, or a cold load of `#connections` would be rejected before the
  // session says whether SMB is on. Once it does say, a Connections deep link
  // for a user without SMB settles on Account instead of falling through to
  // another tab's content under a heading nobody selected.
  $effect(() => {
    if (tab === 'connections' && features && !features.smb) tab = 'account'
  })

  async function doLogout(): Promise<void> {
    if (loggingOut) return
    loggingOut = true
    try {
      await api.logout()
    } catch {
      // best-effort: fall through to the login screen either way, the
      // session cookie is either gone server-side or already unusable
    } finally {
      setAnonymous()
      loggingOut = false
      await goto('/login')
    }
  }

  /** Re-fetches `GET /api/auth/session` after a settings change that flips one
   *  of its fields (password, TOTP, SMB) so the badges on this page — and
   *  anything else reading `authState.session` — reflect it immediately
   *  without a full reload. */
  async function refreshSession(): Promise<void> {
    try {
      const s = await api.session()
      authState.session = s
    } catch {
      // A refresh failing here isn't fatal — the section that triggered it
      // already told the user its own action succeeded. Worst case the
      // badge is stale until the next natural session fetch.
    }
  }
</script>

<svelte:head><title>{t('settings.settings_stowcloud')}</title></svelte:head>

<div class="sc-settings">
  <div class="sc-settings__title">
    <h1>{t('common.settings')}</h1>
  </div>
  <div class="sc-settings__head">
    <!-- The group name goes on a wrapper, not on `Tabs`: the component spreads
         its extra attributes onto every `<input type="radio">` it renders, so
         an `aria-label` there overrode each tab's own `<label>` and a screen
         reader announced three radios all called "Settings sections". -->
    <div class="sc-settings__head-inner" role="radiogroup" aria-label={t('settings.settings_sections')}>
      <Tabs bind:tab items={tabs} />
    </div>
  </div>

  <div class="sc-settings__inner">
    {#if tab === 'account'}
      <section>
        <h2>{t('settings.account')}</h2>
        {#if user}
          <p class="sc-settings__account-name">{user.display_name || user.name}</p>
        {/if}
        <div class="sc-settings__row">
          <Button variant="outlined" onclick={doLogout} disabled={loggingOut}>{t('common.sign_out')}</Button>
        </div>
      </section>
      <Divider />

      <section>
        <h2>{t('common.password')}</h2>
        <p class="sc-settings__hint">{t('settings.at_least_10_characters_changing')}</p>
        {#await import('../../../lib/ui/settings/PasswordSection.svelte') then mod}
          {@const PasswordSection = mod.default}
          <PasswordSection onchanged={refreshSession} />
        {/await}
      </section>
    {:else if tab === 'security'}
      <section>
        <h2>{t('settings.two_factor_authentication')}</h2>
        <p class="sc-settings__hint">
          {t('settings.asks_6_digit_code_from')}
        </p>
        {#await import('../../../lib/ui/settings/TotpSection.svelte') then mod}
          {@const TotpSection = mod.default}
          <TotpSection enabled={user?.totp_enabled ?? false} onchanged={refreshSession} />
        {/await}
      </section>
      <Divider />

      {#if ssoVisible && ssoConfig}
        <section>
          <h2>{t('settings.single_sign_on')}</h2>
          <p class="sc-settings__hint">
            {t('settings.sign_your_organisations_identity_provider')}
          </p>
          {#await import('../../../lib/ui/settings/OidcSection.svelte') then mod}
            {@const OidcSection = mod.default}
            <OidcSection
              configured={ssoConfig.enabled}
              providerName={ssoConfig.display_name}
              linked={oidc?.linked ?? false}
              subjectHint={oidc?.subject_hint}
              linkedNs={oidc?.linked_ns}
              onchanged={refreshSession}
            />
          {/await}
        </section>
        <Divider />
      {/if}

      <section>
        <h2>{t('settings.app_passwords')}</h2>
        <p class="sc-settings__hint">
          {t('settings.use_one_where_your_account')}
        </p>
        {#await import('../../../lib/ui/settings/AppPasswordsSection.svelte') then mod}
          {@const AppPasswordsSection = mod.default}
          <AppPasswordsSection />
        {/await}
      </section>
      <Divider />

      <section>
        <h2>{t('settings.active_sessions')}</h2>
        <p class="sc-settings__hint">{t('settings.devices_currently_signed_account_sign')}</p>
        {#await import('../../../lib/ui/settings/SessionsSection.svelte') then mod}
          {@const SessionsSection = mod.default}
          <SessionsSection />
        {/await}
      </section>
    {:else if tab === 'connections' && features?.smb}
      <section>
        <h2>SMB</h2>
        <p class="sc-settings__hint">
          {t('settings.mount_as_network_drive_file')}
        </p>
        {#await import('../../../lib/ui/settings/SmbSection.svelte') then mod}
          {@const SmbSection = mod.default}
          <SmbSection
            optOut={user?.smb_opt_out ?? false}
            enabled={user?.smb_enabled ?? false}
            onchanged={refreshSession}
          />
        {/await}
      </section>
    {:else}
      <section>
        <h2>{t('settings.theme')}</h2>
        <div class="sc-settings__row">
          <!-- MD3's connected button group is the control for a single choice
               out of a small set. It replaces three loose buttons whose only
               signal of the current choice was `filled` vs `outlined` -- a
               screen reader read three identical buttons and never said which
               one was on, which `pressed` now answers. `square` because the
               group owns the outer rounding; without it every segment keeps
               its own full radius and the group is three pills, not a bar. -->
          <ConnectedButtons role="group" aria-label={t('settings.theme')}>
            <Button
              square
              variant={uiState.theme === 'system' ? 'filled' : 'tonal'}
              pressed={uiState.theme === 'system'}
              onclick={() => setTheme('system')}>{t('common.system')}</Button
            >
            <Button
              square
              variant={uiState.theme === 'light' ? 'filled' : 'tonal'}
              pressed={uiState.theme === 'light'}
              onclick={() => setTheme('light')}>{t('settings.light')}</Button
            >
            <Button
              square
              variant={uiState.theme === 'dark' ? 'filled' : 'tonal'}
              pressed={uiState.theme === 'dark'}
              onclick={() => setTheme('dark')}>{t('settings.dark')}</Button
            >
          </ConnectedButtons>
        </div>
        <p class="sc-settings__hint">
          {t('settings.choosing_system_follows_your_device')}
        </p>
      </section>
      <Divider />

      <section>
        <h2>{t('settings.language')}</h2>
        <div class="sc-settings__row">
          <ConnectedButtons role="group" aria-label={t('settings.language')}>
            <!-- Each option is written in its own language, never translated:
                 someone stuck in a language they cannot read has to be able to
                 find their way out of it. -->
            <Button
              square
              variant={currentLocale() === 'ko' ? 'filled' : 'tonal'}
              pressed={currentLocale() === 'ko'}
              onclick={() => setLocale('ko')}>한국어</Button
            >
            <Button
              square
              variant={currentLocale() === 'en' ? 'filled' : 'tonal'}
              pressed={currentLocale() === 'en'}
              onclick={() => setLocale('en')}>English</Button
            >
          </ConnectedButtons>
        </div>
        <p class="sc-settings__hint">
          {t('settings.language_choice_stays_this_browser')}
        </p>
      </section>
    {/if}
  </div>
</div>

<style>
  /* `.sc-settings` (outer) owns the scroll and spans the FULL width of
   * `.sc-app-shell__main` — `main` is a column flexbox, so this stretches
   * full width for free via the default `align-items: stretch`. It is only
   * a scroll owner today because each page currently scrolls itself; the
   * shell may later move that ownership to the document (so the mobile
   * address bar can collapse) — if/when that happens, this rule is the only
   * one that needs to change (delete `overflow-y: auto` here), since the
   * centered column below is a separate element and doesn't care which
   * ancestor scrolls.
   *
   * `.sc-settings__inner` is the actual reading column: capped and
   * *centered* (`margin-inline: auto`), not pinned to the left edge of the
   * (much wider, at desktop widths) scroll owner. It previously had no
   * inner wrapper at all — `.sc-settings` itself carried both the
   * max-width and the padding with `margin-inline` left at its default of
   * 0, which put the whole page flush against the nav rail and left
   * everything past 640px (736px of a 1440px window) as dead space, with
   * the scrollbar rendered by the browser at the *content's* right edge —
   * i.e. floating mid-window, not at the window edge where a scrollbar
   * reads as "the page has more below," not "the layout stopped halfway."
   * The cap itself is also widened from 640 to a bit under 1000: 640 was
   * reading-measure-only sizing applied to the whole pane, including rows
   * (app passwords, sessions) and, on `/admin`, a user table — content
   * that isn't prose and benefits from more width. Individual prose
   * elements (`.sc-settings__hint`) keep their own narrower measure. */
  .sc-settings {
    overflow-y: auto;
    word-break: keep-all;
  }
  .sc-settings__title,
  .sc-settings__head-inner,
  .sc-settings__inner {
    max-width: min(960px, 100%);
    margin-inline: auto;
    padding-inline: var(--sc-page-pad);
  }
  .sc-settings__title {
    padding-block: var(--sc-page-pad) 0;
  }
  /* MD3's pattern for a tabbed screen: the title scrolls away, the tabs pin.
   * Sticky resolves against `.sc-settings` (the scroll owner two levels up),
   * so the outer band is what carries the sticky and the background — the
   * inner column only carries the horizontal inset, otherwise content would
   * pass through the padding strips on either side of the capped column. */
  .sc-settings__head {
    position: sticky;
    top: 0;
    z-index: 2;
    background: var(--m3c-surface);
  }
  .sc-settings__head-inner {
    padding-inline: 0;
  }
  .sc-settings__inner {
    display: flex;
    flex-direction: column;
    gap: 24px;
    padding-block: 24px var(--sc-page-pad);
  }
  /* global.css resets h1..h6 margin to 0 but never sets font-size or
   * line-height, so headings fell back to the browser's UA default
   * font-size (h1 ~32px, h2 ~24px) while still *inheriting* body's
   * line-height (24px, an MD3 body-large value meant for paragraph text).
   * A 32px-tall glyph in a 24px line box overflows its own box top and
   * bottom — with zero margin against the next element (the Divider), that
   * overflow visibly runs through the divider's rule and into the next
   * heading. Every heading in this page needs an explicit type-scale pair;
   * StorageIndexSection's h3 and this page's own admin sibling already do
   * this correctly, this page's h1/h2 just never got it. */
  .sc-settings__title h1 {
    margin: 0 0 16px;
    @apply --m3-headline-small;
  }
  .sc-settings__inner h2 {
    margin: 0 0 8px;
    @apply --m3-title-large;
  }
  .sc-settings__row {
    display: flex;
    gap: 8px;
    margin-block: 16px;
  }
  /* Dropped only where the row ends its section. The space after a section
     comes from the flex `gap` on `.sc-settings__inner`, and a bottom margin
     here does not collapse out of the section, so it stacked on that gap and
     left 16px above the sign-out button and 40px below it. Where the row is
     followed by its own hint, the margin is what separates the two. */
  .sc-settings__row:last-child {
    margin-block-end: 0;
  }
  .sc-settings__hint {
    max-width: 480px;
    margin: 0 0 16px;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
  .sc-settings__account-name {
    margin: 0 0 8px;
    @apply --m3-body-large;
  }
</style>
