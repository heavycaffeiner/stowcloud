<script lang="ts">
  // SMB. One rule the user actually sees: the web UI password and the SMB
  // password are the same password, unless they deliberately separate them,
  // and every path back returns them to being the same.
  //
// Three user toggles now:
//   smb_enabled: the "publish" half.
//   smb_opt_out: refuses NT hash derivation entirely. Turning it on
//                erases the stored credential immediately.
  //   a separate SMB-only password, which is what makes SMB reachable for an
  //   account whose account password stopped being an SMB credential.
  import { createMutation } from '@tanstack/svelte-query'
  import { t } from '../../i18n'
  import { ApiError } from '../../api/client'
  import { describeApiError } from '../../api/error-text'
  import Button from '../Button.svelte'
  import Dialog from '../Dialog.svelte'
  import Switch from '../Switch.svelte'
  import TextField from '../TextField.svelte'
  import { createSession } from '../../query/session'
  import { smbPasswordMutation, smbSettingsMutation } from '../../query/account'

  const session = createSession()
  const optOut = $derived(session.data?.user.smb_opt_out ?? false)
  const enabled = $derived(session.data?.user.smb_enabled ?? false)
  /** What works over SMB right now, with the deployment's TOTP policy
   *  already folded in server-side. */
  const credential = $derived(session.data?.user.smb_credential ?? 'none')
  const reason = $derived(session.data?.user.smb_unavailable_reason)
  const totpEnabled = $derived(session.data?.user.totp_enabled ?? false)
  const oidcLinked = $derived(session.data?.oidc.linked ?? false)

  /** This section's own live region. `UploadTray`'s is not borrowed for it: a
   *  per-route Snackbar goes silent on navigation. */
  let announcement = $state('')

  const hasDedicated = $derived(credential === 'dedicated')
  /** Whether removing the separate password leaves any SMB credential at all.
   *  The same rule the server applies, so the dialog can say which of the two
   *  outcomes is about to happen before the user commits. */
  const revertsToAccount = $derived(!totpEnabled && !oidcLinked && !optOut && enabled)

  /**
   * Four of these five lines answer the same question, "SMB does not work",
   * and the reason they are not one line is that the way back differs each
   * time. The server reports one `opted_out` for both switches and one
   * `not_set` for every account with no credential; the four booleans it takes
   * to tell them apart are already on the session, so the split lives here
   * rather than in a wider wire vocabulary.
   *
   * The two that are easy to get wrong:
   *
   * - `smb_enabled` off keeps the stored credential and only withholds it from
   *   the published file, so "not storing credentials" would be false for it,
   *   and flipping the switch back works immediately.
   * - `smb_opt_out` erases it. Turning the switch off restores nothing by
   *   itself: the hash comes back on the next successful password sign-in,
   *   which is where `maybe_backfill_nt` runs.
   */
  const stateLine = $derived.by(() => {
    if (credential === 'account') return t('smb.uses_account_password')
    if (credential === 'dedicated') return t('smb.uses_separate_password')
    if (reason === 'totp_blocked') return t('smb.unavailable_totp_blocked')
    if (optOut) return t('smb.unavailable_opted_out')
    if (!enabled) return t('smb.unavailable_not_published')
    // No credential, and nothing is withholding one. Either a second factor
    // closed the account password (only a separate password reopens it), or
    // the account is eligible and simply has not signed in since.
    if (totpEnabled || oidcLinked) return t('smb.unavailable_not_set')
    return t('smb.unavailable_awaiting_sign_in')
  })

  // ── toggle access switches (reconfirming with account password) ──
  let toggleOpen = $state(false)
  let toggleCurrent = $state('')
  let pendingOptOut = $state(false)
  let pendingEnabled = $state(false)
  const toggle = createMutation(() => smbSettingsMutation())
  const toggleError = $derived.by(() => {
    const err = toggle.error
    if (!err) return null
    return err instanceof ApiError && err.code === 'auth.invalid_credentials'
      ? t('common.incorrect_password')
      : describeApiError(err, t('smb.could_not_save_setting_try'))
  })

  function openToggle(nextOptOut: boolean, nextEnabled: boolean): void {
    pendingOptOut = nextOptOut
    pendingEnabled = nextEnabled
    toggleCurrent = ''
    toggle.reset()
    toggleOpen = true
  }

  function closeToggle(): void {
    toggleOpen = false
  }

  function confirmToggle(): void {
    toggle.mutate(
      { currentPassword: toggleCurrent, optOut: pendingOptOut, enabled: pendingEnabled },
      { onSuccess: () => { toggleOpen = false } }
    )
  }

  function onToggleEnabled(checked: boolean): void {
    openToggle(optOut, checked)
  }

  function onToggleOptOut(checked: boolean): void {
    openToggle(checked, checked ? false : enabled)
  }
  // ── set a separate password ──
  let setOpen = $state(false)
  let setCurrent = $state('')
  let setNew = $state('')
  const setPw = createMutation(() => smbPasswordMutation())
  const setError = $derived.by(() => {
    const err = setPw.error
    if (!err) return null
    if (err instanceof ApiError && err.code === 'auth.invalid_credentials') return t('common.incorrect_password')
    if (err instanceof ApiError && err.code === 'auth.weak_password') {
      return t('smb.password_too_short', { min: err.reasonNumber('min_length') ?? 10 })
    }
    return describeApiError(err, t('smb.could_not_save_password'))
  })

  function openSet(): void {
    setCurrent = ''
    setNew = ''
    setPw.reset()
    setOpen = true
  }

  function closeSet(): void {
    setOpen = false
  }

  function confirmSet(): void {
    setPw.mutate(
      { currentPassword: setCurrent, smbPassword: setNew },
      {
        onSuccess: (res) => {
          setOpen = false
          announcement =
            'smb_toggles_cleared' in res && res.smb_toggles_cleared
              ? `${t('smb.password_set')} ${t('smb.toggles_cleared')}`
              : t('smb.password_set')
        }
      }
    )
  }

  // ── remove it ──
  let clearOpen = $state(false)
  let clearCurrent = $state('')
  const clearPw = createMutation(() => smbPasswordMutation())
  const clearError = $derived.by(() => {
    const err = clearPw.error
    if (!err) return null
    if (err instanceof ApiError && err.code === 'auth.invalid_credentials') return t('common.incorrect_password')
    if (err instanceof ApiError && err.status === 404) return t('smb.no_separate_password_set')
    return describeApiError(err, t('smb.could_not_save_password'))
  })

  function openClear(): void {
    clearCurrent = ''
    clearPw.reset()
    clearOpen = true
  }

  function closeClear(): void {
    clearOpen = false
  }

  function confirmClear(): void {
    clearPw.mutate(
      { currentPassword: clearCurrent, smbPassword: null },
      {
        onSuccess: (res) => {
          clearOpen = false
          announcement =
            'reverted_to_account_password' in res && res.reverted_to_account_password
              ? t('smb.password_removed_reverted')
              : t('smb.password_removed_no_access')
        }
      }
    )
  }
</script>

<div class="sc-smb">
  <p class="sc-smb__note">
    {t('smb.smb_reachable_only_from_local')}
  </p>
  <p class="sc-smb__state">{stateLine}</p>
  <div class="sc-smb__row">
    <Switch checked={enabled} label={t('smb.allow_smb_access')} onchange={onToggleEnabled} />
  </div>
  <div class="sc-smb__row">
    <Switch checked={optOut} label={t('smb.do_not_store_smb_credentials')} onchange={onToggleOptOut} />
  </div>
  <div class="sc-smb__actions">
    <Button variant={hasDedicated ? 'outlined' : 'filled'} onclick={openSet}>
      {hasDedicated ? t('smb.change_separate_password') : t('smb.set_separate_password')}
    </Button>
    {#if hasDedicated}
      <Button variant="text" onclick={openClear}>{t('smb.remove_separate_password')}</Button>
    {/if}
  </div>
  <p class="sc-smb__announce" aria-live="polite">{announcement}</p>
</div>

<Dialog open={setOpen} title={t('smb.set_password_title')} onclose={closeSet}>
  <p>{t('smb.set_password_hint')}</p>
  <TextField
    type="password"
    label={t('common.current_password')}
    bind:value={setCurrent}
    autocomplete="current-password"
  />
  <TextField
    type="password"
    label={t('smb.new_smb_password')}
    bind:value={setNew}
    error={setError}
    autocomplete="new-password"
  />
  {#snippet actions()}
    <Button variant="text" onclick={closeSet}>{t('common.cancel')}</Button>
    <Button variant="filled" disabled={!setCurrent || !setNew} loading={setPw.isPending} onclick={confirmSet}>
      {t('common.save')}
    </Button>
  {/snippet}
</Dialog>

<Dialog open={clearOpen} title={t('smb.remove_password_title')} onclose={closeClear}>
  <p>{revertsToAccount ? t('smb.remove_reverts_to_account') : t('smb.remove_ends_access')}</p>
  <TextField
    type="password"
    label={t('common.current_password')}
    bind:value={clearCurrent}
    error={clearError}
    autocomplete="current-password"
  />
  {#snippet actions()}
    <Button variant="text" onclick={closeClear}>{t('common.cancel')}</Button>
    <Button variant="filled" disabled={!clearCurrent} loading={clearPw.isPending} onclick={confirmClear}>
      {t('common.remove_2')}
    </Button>
  {/snippet}
</Dialog>

<Dialog open={toggleOpen} title={t('common.settings')} onclose={closeToggle}>
  <p>{t('smb.set_password_hint')}</p>
  <TextField
    type="password"
    label={t('common.current_password')}
    bind:value={toggleCurrent}
    error={toggleError}
    autocomplete="current-password"
  />
  {#snippet actions()}
    <Button variant="text" onclick={closeToggle}>{t('common.cancel')}</Button>
    <Button variant="filled" disabled={!toggleCurrent} loading={toggle.isPending} onclick={confirmToggle}>
      {t('common.save')}
    </Button>
  {/snippet}
</Dialog>

<style>
  .sc-smb {
    display: flex;
    flex-direction: column;
    gap: 16px;
    max-width: 480px;
  }
  .sc-smb__note {
    margin: 0;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
  .sc-smb__state {
    margin: 0;
    padding: 12px;
    border-radius: var(--m3-shape-extra-small);
    background: var(--m3c-surface-container-highest);
    color: var(--m3c-on-surface);
    @apply --m3-body-medium;
  }
  .sc-smb__row {
    display: flex;
    align-items: center;
  }
  .sc-smb__actions {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px;
  }
  .sc-smb__announce {
    margin: 0;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
</style>
