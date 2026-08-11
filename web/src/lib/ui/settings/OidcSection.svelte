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
  import { formatDateNs, t } from '../../i18n'
  import { page } from '$app/state'
  import { api, ApiError } from '../../api/client'
  import { oidcErrorMessage } from '../../api/oidc'
  import Button from '../Button.svelte'
  import Dialog from '../Dialog.svelte'
  import TextField from '../TextField.svelte'
  import { Icon } from 'm3-svelte'
  import { icons } from '../../icons'

  interface Props {
    /** `GET /api/auth/oidc/config`'s `enabled`. False with `linked` true is a
     *  real state, not a contradiction: an account stays attached to a
     *  provider the deployment has since switched off, and that is exactly
     *  when being able to disconnect matters. */
    configured: boolean
    /** `display_name`. An administrator types it and may leave it empty, so no
     *  message may assume it is there. */
    providerName: string
    /** From `GET /api/auth/session`'s `oidc` object. */
    linked: boolean
    subjectHint?: string
    linkedNs?: string
    /** Whether this account holds a separate SMB-only password. Disconnecting
     *  is the exact undo of the link that closed the account password as an
     *  SMB credential, so it replaces one, and a credential the user set is
     *  never removed in silence. */
    smbDedicated?: boolean
    onchanged?: () => void
  }
  let {
    configured,
    providerName,
    linked,
    subjectHint,
    linkedNs,
    smbDedicated = false,
    onchanged
  }: Props = $props()

  // §5-2 table B: a link-mode callback that failed redirects here with the
  // code in the query string. `+page.svelte`'s tab sync leaves the query alone,
  // so this survives the hash rewrite that lands on the security tab.
  const flowError = $derived(oidcErrorMessage(page.url.searchParams.get('oidc_error')))

  const providerLabel = $derived(providerName || t('oidc.identity_provider'))

  let connectOpen = $state(false)
  let connectPassword = $state('')
  let connectError = $state<string | null>(null)
  let connecting = $state(false)

  let disconnectOpen = $state(false)
  let disconnectPassword = $state('')
  let disconnectError = $state<string | null>(null)
  let disconnecting = $state(false)
  /** This section's own live region, for the one thing disconnecting does that
   *  the user did not come here to do. */
  let smbAnnouncement = $state('')

  function openConnect(): void {
    connectPassword = ''
    connectError = null
    connectOpen = true
  }

  function closeConnect(): void {
    if (connecting) return
    connectOpen = false
    connectPassword = ''
    connectError = null
  }

  async function confirmConnect(): Promise<void> {
    connectError = null
    connecting = true
    try {
      const { authorize_url } = await api.oidcLinkStart(connectPassword)
      // The browser has to actually arrive at the provider's login page, so
      // this is a full navigation. `authorize_url` comes from the server's own
      // URL builder, never from anything typed here.
      //
      // No `returnTo` was sent: with none, the server lands a finished link
      // flow back on this screen, which is where it started.
      window.location.href = authorize_url
    } catch (err) {
      connectError = describeError(err, t('oidc.could_not_start_connection_try'))
      connecting = false
    }
  }

  function openDisconnect(): void {
    disconnectPassword = ''
    disconnectError = null
    disconnectOpen = true
  }

  function closeDisconnect(): void {
    if (disconnecting) return
    disconnectOpen = false
    disconnectPassword = ''
    disconnectError = null
  }

  async function confirmDisconnect(): Promise<void> {
    disconnectError = null
    disconnecting = true
    try {
      const res = await api.oidcUnlink(disconnectPassword)
      disconnectOpen = false
      smbAnnouncement = res.smb_password_replaced ? t('smb.replaced_by_account_password') : ''
      onchanged?.()
    } catch (err) {
      disconnectError = describeError(err, t('oidc.could_not_disconnect_try_again'))
    } finally {
      disconnecting = false
    }
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
    return fallback
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
  <p class="sc-oidc__detail" aria-live="polite">{smbAnnouncement}</p>
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
    <Button variant="text" onclick={closeConnect} disabled={connecting}>{t('common.cancel')}</Button>
    <Button variant="filled" disabled={!connectPassword} loading={connecting} onclick={confirmConnect}>
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
    <Button variant="text" onclick={closeDisconnect} disabled={disconnecting}>{t('common.cancel')}</Button>
    <Button variant="filled" disabled={!disconnectPassword} loading={disconnecting} onclick={confirmDisconnect}>
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
