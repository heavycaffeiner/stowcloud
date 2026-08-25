<script lang="ts">
  // One account's single sign-on link, from the administrator's side
  // (`docs/proposals/stowcloud-0-oidc-login.md` §5-1's three admin routes).
  //
  // This is the recovery path, and it only disconnects. Attaching an identity
  // is the owner's own `POST /api/auth/oidc/link/start`, which charges their
  // password and walks the real flow; the server refuses to do it from here,
  // so there is no form for it. Unlinking here cannot put the SMB credential
  // back either: there is no plaintext to re-derive it from, and the
  // confirmation says so out loud.
  //
  // Unlike the account's own screen, this shows the *whole* subject. Somebody
  // working out why a person cannot sign in needs the exact string to compare
  // against what the provider shows.
  import { formatDateNs, t } from '../../i18n'
  import { api, ApiError, type AdminUser, type AdminUserOidc } from '../../api/client'
  import Button from '../Button.svelte'
  import Dialog from '../Dialog.svelte'
  import ProgressCircular from '../ProgressCircular.svelte'
  import { Icon } from 'm3-svelte'
  import { icons } from '../../icons'

  interface Props {
    /** `null` closes the dialog. */
    user: AdminUser | null
    onclose: () => void
  }
  let { user, onclose }: Props = $props()

  // Named `link`, not `state`: a variable called `state` shadows the `$state`
  // rune for the rest of the file.
  let link = $state<AdminUserOidc | null>(null)
  let loading = $state(false)
  let loadError = $state<string | null>(null)


  let confirmUnlink = $state(false)
  let unlinking = $state(false)
  let unlinkError = $state<string | null>(null)
  /** Set once an unlink has succeeded, so the SMB consequence stays on screen
   *  after the dialog stops offering the button that caused it. */
  let unlinked = $state(false)

  // Reloads whenever a different account is opened. Keyed on the id rather
  // than on the object so reopening the same account after a change in the
  // list behind it does not refetch for no reason.
  let loadedFor = $state<number | null>(null)
  $effect(() => {
    const id = user?.id ?? null
    if (id === null || id === loadedFor) return
    loadedFor = id
    void load(id)
  })

  async function load(id: number): Promise<void> {
    loading = true
    loadError = null
    unlinkError = null
    confirmUnlink = false
    unlinked = false
    try {
      link = await api.adminGetUserOidc(id)
    } catch (err) {
      link = null
      loadError = describeError(err, t('oidc.could_not_load_connection_status'))
    } finally {
      loading = false
    }
  }

  function describeError(err: unknown, fallback: string): string {
    if (err instanceof ApiError) {
      switch (err.code) {
        case 'oidc.disabled':
          return t('oidc.single_sign_not_configured')
        case 'oidc.invalid_subject':
          return t('oidc.enter_identifier')
        case 'oidc.subject_already_linked':
          return t('oidc.identity_already_connected_another_account')
        case 'oidc.not_linked':
          return t('oidc.account_has_no_connected_identity')
      }
    }
    return fallback
  }

  async function submitUnlink(): Promise<void> {
    if (!user) return
    unlinkError = null
    unlinking = true
    try {
      await api.adminUnlinkUserOidc(user.id)
      link = { linked: false, issuer: null, subject: null, linked_ns: null, last_login_ns: null }
      confirmUnlink = false
      unlinked = true
    } catch (err) {
      unlinkError = describeError(err, t('oidc.could_not_disconnect_try_again'))
    } finally {
      unlinking = false
    }
  }
</script>

<Dialog
  open={!!user}
  title={user ? t('oidc.single_sign_for', { name: user.display_name || user.name }) : t('settings.single_sign_on')}
  onclose={onclose}
>
  {#if loading}
    <ProgressCircular />
  {:else if loadError}
    <p class="sc-user-oidc__error" role="alert">{loadError}</p>
  {:else if link?.linked}
    <dl class="sc-user-oidc__facts">
      <div>
        <dt>{t('oidc.provider')}</dt>
        <dd>{link.issuer}</dd>
      </div>
      <div>
        <dt>{t('oidc.subject_sub')}</dt>
        <dd><code>{link.subject}</code></dd>
      </div>
      <div>
        <dt>{t('oidc.linked_on')}</dt>
        <dd>{link.linked_ns ? formatDateNs(link.linked_ns) : '-'}</dd>
      </div>
      <div>
        <dt>{t('oidc.last_sign')}</dt>
        <dd>{link.last_login_ns ? formatDateNs(link.last_login_ns) : t('oidc.never')}</dd>
      </div>
    </dl>

    {#if confirmUnlink}
      <p class="sc-user-oidc__warning">
        <Icon icon={icons.warning} size={16} />
        {t('oidc.disconnecting_here_cannot_restore_smb')}
      </p>
      {#if unlinkError}<p class="sc-user-oidc__error" role="alert">{unlinkError}</p>{/if}
    {:else}
      <p class="sc-user-oidc__hint">{t('oidc.disconnecting_signs_account_out_every')}</p>
    {/if}
  {:else}
    {#if unlinked}
      <p class="sc-user-oidc__warning">
        <Icon icon={icons.warning} size={16} />
        {t('oidc.disconnected_smb_access_account_will')}
      </p>
    {/if}
    <p class="sc-user-oidc__hint">{t('oidc.no_identity_connected_user_must_link')}</p>
  {/if}

  {#snippet actions()}
    {#if link?.linked}
      {#if confirmUnlink}
        <Button variant="text" onclick={() => (confirmUnlink = false)} disabled={unlinking}>{t('common.cancel')}</Button>
        <Button variant="filled" danger loading={unlinking} onclick={submitUnlink}>{t('oidc.disconnect_anyway')}</Button>
      {:else}
        <Button variant="text" onclick={onclose}>{t('common.close')}</Button>
        <Button variant="outlined" danger onclick={() => (confirmUnlink = true)}>{t('oidc.disconnect')}</Button>
      {/if}
    {:else if loading || loadError}
      <Button variant="text" onclick={onclose}>{t('common.close')}</Button>
    {:else}
      <Button variant="text" onclick={onclose}>{t('common.close')}</Button>
    {/if}
  {/snippet}
</Dialog>

<style>
  .sc-user-oidc__facts {
    display: flex;
    flex-direction: column;
    gap: 8px;
    margin: 0 0 16px;
  }
  .sc-user-oidc__facts dt {
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
  .sc-user-oidc__facts dd {
    margin: 0;
    /* A subject is an opaque provider-side identifier with no break
       opportunities of its own, and some are long enough to push the dialog
       wider than the viewport. */
    overflow-wrap: anywhere;
    @apply --m3-body-medium;
  }
  .sc-user-oidc__hint {
    margin: 0 0 8px;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
  .sc-user-oidc__error {
    margin: 0;
    color: var(--m3c-error);
    @apply --m3-body-small;
  }
  .sc-user-oidc__warning {
    display: flex;
    align-items: flex-start;
    gap: 8px;
    margin: 0 0 16px;
    padding: 12px;
    border-radius: var(--m3-shape-extra-small);
    background: var(--m3c-error-container);
    color: var(--m3c-on-error-container);
    @apply --m3-body-medium;
  }
</style>
