<script lang="ts">
  // Single sign-on, self-service half (`docs/proposals/stowcloud-0-oidc-login.md`
  // §4.3.2). Connect this account to an identity at the deployment's provider,
  // or disconnect it again.
  //
  // Both directions charge the account password, which is not the usual
  // "confirm you meant it". Connecting *adds a permanent credential*: somebody
  // with a few seconds at an unlocked screen could otherwise attach their own
  // identity and still be getting in after the owner changes their password
  // and revokes every session. charges a password for
  // turning TOTP on *and* off for exactly that reason, and TotpSection beside
  // this one is the pattern being followed.
  //
  // Disconnecting needs the plaintext for a second reason: linking deletes the
  // SMB NT hash derived from the account password (§4.3.6), and the password is
  // the only thing that can derive it again.
  import { createMutation, createQuery } from '@tanstack/svelte-query'
  import { formatDateNs, t } from '../../i18n'
  import { page } from '$app/state'
  import { ApiError } from '../../api/client'
  import { describeApiError } from '../../api/error-text'
  import { oidcErrorMessage } from '../../api/oidc'
  import Button from '../Button.svelte'
  import Dialog from '../Dialog.svelte'
  import TextField from '../TextField.svelte'
  import { Icon } from 'm3-svelte'
  import { icons } from '../../icons'
  import { createSession, oidcConfigQuery } from '../../query/session'
  import { oidcLinkStartMutation, oidcUnlinkMutation } from '../../query/account'

  const session = createSession()
  const oidcConfig = createQuery(() => oidcConfigQuery())

  /** `GET /api/auth/oidc/config`'s `enabled`. False with `linked` true is a
   *  real state, not a contradiction: an account stays attached to a
   *  provider the deployment has since switched off, and that is exactly
   *  when being able to disconnect matters. */
  const configured = $derived(oidcConfig.data?.enabled ?? false)
  /** An administrator types `display_name` and may leave it empty, so no
   *  message may assume it is there. */
  const providerLabel = $derived(oidcConfig.data?.display_name || t('oidc.identity_provider'))

  /** From `GET /api/auth/session`'s `oidc` object. */
  const linked = $derived(session.data?.oidc.linked ?? false)
  const subjectHint = $derived(session.data?.oidc.subject_hint)
  const linkedNs = $derived(session.data?.oidc.linked_ns)
  /** Whether this account holds a separate SMB-only password. Disconnecting
   *  is the exact undo of the link that closed the account password as an
   *  SMB credential, so it replaces one, and a credential the user set is
   *  never removed in silence. */
  const smbDedicated = $derived(session.data?.user.smb_credential === 'dedicated')

  // §5-2 table B: a link-mode callback that failed redirects here with the
  // code in the query string. `+page.svelte`'s tab sync leaves the query alone,
  // so this survives the hash rewrite that lands on the security tab.
  const flowError = $derived(oidcErrorMessage(page.url.searchParams.get('oidc_error')))

  let connectOpen = $state(false)
  let connectPassword = $state('')
  const link = createMutation(() => oidcLinkStartMutation())
  const connectError = $derived(link.error ? describeError(link.error, t('oidc.could_not_start_connection_try')) : null)

  let disconnectOpen = $state(false)
  let disconnectPassword = $state('')
  const unlink = createMutation(() => oidcUnlinkMutation())
  const disconnectError = $derived(unlink.error ? describeError(unlink.error, t('oidc.could_not_disconnect_try_again')) : null)

  function openConnect(): void {
    connectPassword = ''
    link.reset()
    connectOpen = true
  }

  function closeConnect(): void {
    if (link.isPending) return
    connectOpen = false
    connectPassword = ''
  }

  function confirmConnect(): void {
    link.mutate(
      // Where the finished flow should land. Named explicitly, because the
      // server's default is the root: without it, connecting a provider from
      // the settings screen dropped the user on the file browser.
      { password: connectPassword, returnTo: window.location.pathname },
      {
        onSuccess: ({ authorize_url }) => {
          // The browser has to actually arrive at the provider's login page,
          // so this is a full navigation. `authorize_url` comes from the
          // server's own URL builder, never from anything typed here.
          window.location.href = authorize_url
        }
      }
    )
  }

  function openDisconnect(): void {
    disconnectPassword = ''
    unlink.reset()
    disconnectOpen = true
  }

  function closeDisconnect(): void {
    if (unlink.isPending) return
    disconnectOpen = false
    disconnectPassword = ''
  }

  function confirmDisconnect(): void {
    unlink.mutate(disconnectPassword, {
      onSuccess: () => {
        disconnectOpen = false
        // No announcement about the file-sharing password: the server does
        // not report one, and it could not. Enrolling or disconnecting
        // changes what is published, never what is stored, and a single
        // row holds the hash whether it came from the account password or
        // a separate one, so the two are indistinguishable afterwards. The
        // sharing section below reads the real state.
      }
    })
  }

  function describeError(err: unknown, fallback: string): string {
    if (err instanceof ApiError) {
      switch (err.code) {
        case 'auth.invalid_credentials':
          return t('common.incorrect_password')
        case 'oidc.disabled':
          return t('oidc.single_sign_not_configured')
        case 'oidc.provider_unavailable':
          return t('oidc.could_not_reach_identity_provider')
        case 'oidc.not_linked':
          return t('oidc.account_has_no_connected_identity')
      }
    }
    return describeApiError(err, fallback)
  }
</script>

<div class="sc-oidc">
  {#if flowError}
    <p class="sc-oidc__error" role="alert">{flowError}</p>
  {/if}

  <div class="sc-oidc__status">
    <span class="sc-oidc__badge" class:sc-oidc__badge--on={linked}>
      {linked ? t('oidc.connected') : t('oidc.not_connected')}
    </span>
    {#if linked}
      <Button variant="outlined" onclick={openDisconnect}>{t('oidc.disconnect')}</Button>
    {:else if configured}
      <Button variant="filled" onclick={openConnect}>{t('oidc.connect_provider', { provider: providerLabel })}</Button>
    {/if}
  </div>

  {#if linked}
    <p class="sc-oidc__detail">
      {#if subjectHint}{t('oidc.identity', { subject: subjectHint })}{/if}
      {#if linkedNs}{t('oidc.connected_on', { date: formatDateNs(linkedNs) })}{/if}
    </p>
    {#if !configured}
      <!-- Attached to a provider the deployment has since switched off. The
           link still governs SMB and WebDAV Basic, so saying nothing would
           leave a person with no explanation for an account password that
           stopped working on those. -->
      <p class="sc-oidc__detail">{t('oidc.single_sign_currently_switched_off')}</p>
    {/if}
  {:else if configured}
    <p class="sc-oidc__detail">{t('oidc.connect_sign_instead_your_account', { provider: providerLabel })}</p>
  {/if}
</div>

<Dialog open={connectOpen} title={t('oidc.connect_provider', { provider: providerLabel })} onclose={closeConnect}>
  <p>{t('oidc.after_you_confirm_password_taken')}</p>
  <p class="sc-oidc__warning">
    <Icon icon={icons.warning} size={16} />
    {t('oidc.connecting_closes_smb_access_account')}
  </p>
  <TextField
    type="password"
    label={t('common.current_password')}
    bind:value={connectPassword}
    error={connectError}
    autocomplete="current-password"
  />
  {#snippet actions()}
    <Button variant="text" onclick={closeConnect} disabled={link.isPending}>{t('common.cancel')}</Button>
    <Button variant="filled" disabled={!connectPassword} loading={link.isPending} onclick={confirmConnect}>
      {t('common.continue')}
    </Button>
  {/snippet}
</Dialog>

<Dialog open={disconnectOpen} title={t('oidc.disconnect_single_sign')} onclose={closeDisconnect}>
  <p>{t('oidc.you_sign_your_account_password')}</p>
  <p class="sc-oidc__warning">
    <Icon icon={icons.warning} size={16} />
    {t('oidc.every_session_opened_through_signed')}
  </p>
  {#if smbDedicated}
    <p class="sc-oidc__detail">{t('smb.dedicated_will_be_replaced')}</p>
  {/if}
  <TextField
    type="password"
    label={t('common.current_password')}
    bind:value={disconnectPassword}
    error={disconnectError}
    autocomplete="current-password"
  />
  {#snippet actions()}
    <Button variant="text" onclick={closeDisconnect} disabled={unlink.isPending}>{t('common.cancel')}</Button>
    <Button variant="filled" disabled={!disconnectPassword} loading={unlink.isPending} onclick={confirmDisconnect}>
      {t('oidc.disconnect')}
    </Button>
  {/snippet}
</Dialog>

<style>
  .sc-oidc {
    display: flex;
    flex-direction: column;
    gap: 12px;
    max-width: 480px;
  }
  .sc-oidc__status {
    display: flex;
    align-items: center;
    gap: 16px;
  }
  .sc-oidc__badge {
    display: inline-flex;
    align-items: center;
    height: 32px;
    padding-inline: 12px;
    border-radius: var(--m3-shape-full);
    background: var(--m3c-surface-container-highest);
    color: var(--m3c-on-surface-variant);
    @apply --m3-label-large;
    white-space: nowrap;
  }
  .sc-oidc__badge--on {
    background: var(--m3c-primary-container);
    color: var(--m3c-on-primary-container);
  }
  .sc-oidc__detail {
    margin: 0;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
  .sc-oidc__error {
    margin: 0;
    padding: 12px;
    border-radius: var(--m3-shape-extra-small);
    background: var(--m3c-error-container);
    color: var(--m3c-on-error-container);
    @apply --m3-body-medium;
  }
  .sc-oidc__warning {
    display: flex;
    align-items: flex-start;
    gap: 8px;
    padding: 12px;
    border-radius: var(--m3-shape-extra-small);
    background: var(--m3c-error-container);
    color: var(--m3c-on-error-container);
    @apply --m3-body-medium;
  }
</style>
