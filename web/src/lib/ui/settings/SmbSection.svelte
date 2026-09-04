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
  import { t } from '../../i18n'
  import { api, ApiError } from '../../api/client'
  import type { SmbCredential, SmbUnavailableReason } from '../../api/types'
  import Button from '../Button.svelte'
  import Dialog from '../Dialog.svelte'
  import Switch from '../Switch.svelte'
  import TextField from '../TextField.svelte'
  import { useRunesStore } from '../../store/core/bridge.svelte'
  import { createDialogStore } from '../../store/slices/settings.slice'


  interface Props {
    optOut: boolean
    enabled: boolean
    /** What works over SMB right now, with the deployment's TOTP policy
     *  already folded in server-side. */
    credential: SmbCredential
    reason?: SmbUnavailableReason
    totpEnabled: boolean
    oidcLinked: boolean
    onchanged?: () => void
  }
  let { optOut, enabled, credential, reason, totpEnabled, oidcLinked, onchanged }: Props = $props()

  let saving = $state(false)
  let error = $state<string | null>(null)
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
  const toggleModal = createDialogStore()
  const toggleSnap = useRunesStore(toggleModal)
  let toggleCurrent = $state('')
  let toggleError = $state<string | null>(null)
  let pendingOptOut = $state(false)
  let pendingEnabled = $state(false)

  function openToggle(nextOptOut: boolean, nextEnabled: boolean): void {
    pendingOptOut = nextOptOut
    pendingEnabled = nextEnabled
    toggleCurrent = ''
    toggleError = null
    toggleModal.open()
  }

  function closeToggle(): void {
    toggleModal.close()
  }

  async function confirmToggle(): Promise<void> {
    toggleError = null
    toggleModal.submit()
    try {
      await api.updateSmbSettings(toggleCurrent, pendingOptOut, pendingEnabled)
      toggleModal.succeed()
      onchanged?.()
    } catch (err) {
      toggleModal.fail('')
      if (err instanceof ApiError && err.code === 'auth.invalid_credentials') {
        toggleError = t('common.incorrect_password')
      } else {
        toggleError = t('smb.could_not_save_setting_try')
      }
    }
  }

  function onToggleEnabled(checked: boolean): void {
    openToggle(optOut, checked)
  }

  function onToggleOptOut(checked: boolean): void {
    openToggle(checked, checked ? false : enabled)
  }
  // ── set a separate password ──
  const setModal = createDialogStore()
  const setSnap = useRunesStore(setModal)
  let setCurrent = $state('')
  let setNew = $state('')
  let setError = $state<string | null>(null)

  function openSet(): void {
    setCurrent = ''
    setNew = ''
    setError = null
    setModal.open()
  }

  function closeSet(): void {
    setModal.close()
  }

  async function confirmSet(): Promise<void> {
    setError = null
    setModal.submit()
    try {
      const res = await api.setSmbPassword(setCurrent, setNew)
      setModal.succeed()
      announcement = res.smb_toggles_cleared
        ? `${t('smb.password_set')} ${t('smb.toggles_cleared')}`
        : t('smb.password_set')
      onchanged?.()
    } catch (err) {
      setModal.fail('')
      if (err instanceof ApiError && err.code === 'auth.invalid_credentials') {
        setError = t('common.incorrect_password')
      } else if (err instanceof ApiError && err.code === 'auth.weak_password') {
        const min = err.reasonNumber('min_length') ?? 10
        setError = t('smb.password_too_short', { min })
      } else {
        setError = t('smb.could_not_save_password')
      }
    }
  }

  // ── remove it ──
  const clearModal = createDialogStore()
  const clearSnap = useRunesStore(clearModal)
  let clearCurrent = $state('')
  let clearError = $state<string | null>(null)

  function openClear(): void {
    clearCurrent = ''
    clearError = null
    clearModal.open()
  }

  function closeClear(): void {
    clearModal.close()
  }

  async function confirmClear(): Promise<void> {
    clearError = null
    clearModal.submit()
    try {
      const res = await api.clearSmbPassword(clearCurrent)
      clearModal.succeed()
      announcement = res.reverted_to_account_password
        ? t('smb.password_removed_reverted')
        : t('smb.password_removed_no_access')
      onchanged?.()
    } catch (err) {
      clearModal.fail('')
      if (err instanceof ApiError && err.code === 'auth.invalid_credentials') {
        clearError = t('common.incorrect_password')
      } else if (err instanceof ApiError && err.status === 404) {
        clearError = t('smb.no_separate_password_set')
      } else {
        clearError = t('smb.could_not_save_password')
      }
    }
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
  {#if error}<p class="sc-smb__error" role="alert">{error}</p>{/if}
  {#if saving}<p class="sc-smb__saving">{t('common.saving')}</p>{/if}
  <p class="sc-smb__announce" aria-live="polite">{announcement}</p>
</div>

<Dialog open={setSnap.current.isOpen} title={t('smb.set_password_title')} onclose={closeSet}>
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
    <Button variant="filled" disabled={!setCurrent || !setNew} loading={setSnap.current.status === 'submitting'} onclick={confirmSet}>
      {t('common.save')}
    </Button>
  {/snippet}
</Dialog>

<Dialog open={clearSnap.current.isOpen} title={t('smb.remove_password_title')} onclose={closeClear}>
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
    <Button variant="filled" disabled={!clearCurrent} loading={clearSnap.current.status === 'submitting'} onclick={confirmClear}>
      {t('common.remove_2')}
    </Button>
  {/snippet}
</Dialog>

<Dialog open={toggleSnap.current.isOpen} title={t('common.settings')} onclose={closeToggle}>
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
    <Button variant="filled" disabled={!toggleCurrent} loading={toggleSnap.current.status === 'submitting'} onclick={confirmToggle}>
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
  .sc-smb__error {
    margin: 0;
    color: var(--m3c-error);
    @apply --m3-body-small;
  }
  .sc-smb__saving {
    margin: 0;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
  .sc-smb__announce {
    margin: 0;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
</style>
