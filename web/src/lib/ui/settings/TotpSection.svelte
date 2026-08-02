<script lang="ts">
  // Two-factor auth (TOTP) — DESIGN-AUTH.md §6.4: both enabling and
  // disabling require password re-confirmation. Disabling does not require
  // re-login or a password reset — the session stays alive and the modal
  // only asks for one password field (§2.4's "re-authentication is distinct
  // from re-login").
  import { t } from '../../i18n'
  import { api, ApiError } from '../../api/client'
  import Button from '../Button.svelte'
  import TextField from '../TextField.svelte'
  import Dialog from '../Dialog.svelte'
  import { Icon } from 'm3-svelte'
  import { icons } from '../../icons'

  interface Props {
    enabled: boolean
    onchanged?: () => void
  }
  let { enabled, onchanged }: Props = $props()

  // ── enroll flow ──
  let enrollOpen = $state(false)
  let setupSecret = $state('')
  let setupUrl = $state('')
  let setupLoading = $state(false)
  let enrollPassword = $state('')
  let enrollCode = $state('')
  let enrollError = $state<string | null>(null)
  let enrolling = $state(false)
  let recoveryCodes = $state<string[] | null>(null)

  // ── disable flow ──
  let disableOpen = $state(false)
  let disablePassword = $state('')
  let disableError = $state<string | null>(null)
  let disabling = $state(false)

  // ── recovery codes: remaining count + reissue (DESIGN-AUTH.md §6.2) ──
  let recoveryRemaining = $state<number | null>(null)
  let reissueOpen = $state(false)
  let reissuePassword = $state('')
  let reissueError = $state<string | null>(null)
  let reissuing = $state(false)

  // Loads (or reloads) the remaining count whenever TOTP is on -- at mount,
  // right after `confirmEnroll` flips `enabled`, and again after a reissue.
  // Not gated behind a re-confirmation: the count itself isn't a secret from
  // the account's own owner, only the codes are.
  async function loadRecoveryRemaining(): Promise<void> {
    try {
      const res = await api.recoveryCodesRemaining()
      recoveryRemaining = res.remaining
    } catch {
      recoveryRemaining = null
    }
  }

  $effect(() => {
    if (enabled) {
      loadRecoveryRemaining()
    } else {
      recoveryRemaining = null
    }
  })

  function openReissue(): void {
    reissuePassword = ''
    reissueError = null
    reissueOpen = true
  }

  function closeReissue(): void {
    reissueOpen = false
    reissuePassword = ''
    reissueError = null
  }

  async function confirmReissue(): Promise<void> {
    reissueError = null
    reissuing = true
    try {
      const res = await api.reissueRecoveryCodes(reissuePassword)
      // Reuses the same "show once" dialog `confirmEnroll` opens below --
      // a freshly reissued list is exactly as one-shot as a freshly
      // enrolled one, so there is no reason for a second display component.
      recoveryCodes = res.recovery_codes
      reissueOpen = false
      await loadRecoveryRemaining()
    } catch (err) {
      reissueError =
        err instanceof ApiError && err.code === 'auth.invalid_credentials'
          ? t('common.incorrect_password')
          : t('totp.could_not_reissue_them_try')
    } finally {
      reissuing = false
    }
  }

  async function startEnroll(): Promise<void> {
    enrollError = null
    enrollPassword = ''
    enrollCode = ''
    setupLoading = true
    enrollOpen = true
    try {
      const setup = await api.totpSetup()
      setupSecret = setup.secret
      setupUrl = setup.otpauth_url
    } catch {
      enrollError = t('totp.could_not_start_setup_try')
    } finally {
      setupLoading = false
    }
  }

  function closeEnroll(): void {
    enrollOpen = false
    setupSecret = ''
    setupUrl = ''
    enrollPassword = ''
    enrollCode = ''
    enrollError = null
  }

  async function confirmEnroll(): Promise<void> {
    enrollError = null
    enrolling = true
    try {
      const res = await api.totpEnroll(enrollPassword, setupSecret, enrollCode)
      recoveryCodes = res.recovery_codes
      recoveryRemaining = res.recovery_codes.length
      enrollOpen = false
      onchanged?.()
    } catch (err) {
      enrollError =
        err instanceof ApiError && err.code === 'auth.invalid_credentials'
          ? t('totp.password_or_verification_code_incorrect')
          : t('totp.could_not_enable_try_again')
    } finally {
      enrolling = false
    }
  }

  function closeRecoveryCodes(): void {
    recoveryCodes = null
  }

  function openDisable(): void {
    disablePassword = ''
    disableError = null
    disableOpen = true
  }

  function closeDisable(): void {
    disableOpen = false
    disablePassword = ''
    disableError = null
  }

  async function confirmDisable(): Promise<void> {
    disableError = null
    disabling = true
    try {
      await api.totpDisable(disablePassword)
      disableOpen = false
      onchanged?.()
    } catch (err) {
      disableError =
        err instanceof ApiError && err.code === 'auth.invalid_credentials'
          ? t('common.incorrect_password')
          : t('totp.could_not_turn_off_try')
    } finally {
      disabling = false
    }
  }

  async function copySecret(): Promise<void> {
    try {
      await navigator.clipboard.writeText(setupSecret)
    } catch {
      // clipboard API unavailable — the secret is still selectable as text
    }
  }
</script>

<div class="sc-totp">
  <div class="sc-totp__status">
    <span class="sc-totp__badge" class:sc-totp__badge--on={enabled}>
      {enabled ? t('totp.on') : t('totp.off')}
    </span>
    {#if enabled}
      <Button variant="outlined" onclick={openDisable}>{t('totp.turn_off_two_factor_authentication')}</Button>
    {:else}
      <Button variant="filled" onclick={startEnroll}>{t('totp.set_up_two_factor_authentication')}</Button>
    {/if}
  </div>

  {#if enabled}
    <div class="sc-totp__recovery">
      {#if recoveryRemaining !== null}
        <p class="sc-totp__recovery-count" class:sc-totp__recovery-count--low={recoveryRemaining <= 3}>
          {#if recoveryRemaining <= 3}<Icon icon={icons.warning} size={16} />{/if}
          {t('totp.recovery_codes_left', { count: recoveryRemaining })}
          {#if recoveryRemaining <= 3}
            {t('totp.running_low_reissue_them_now')}
          {/if}
        </p>
      {/if}
      <Button variant="outlined" onclick={openReissue}>{t('totp.reissue_recovery_codes')}</Button>
    </div>
  {/if}
</div>

<Dialog open={enrollOpen} title={t('totp.set_up_two_factor_authentication')} onclose={closeEnroll}>
  {#if setupLoading}
    <p>{t('totp.loading_setup_details')}</p>
  {:else}
    <p>{t('totp.add_key_below_authenticator_app')}</p>
    <div class="sc-totp__secret-row">
      <code class="sc-totp__secret">{setupSecret}</code>
      <Button variant="text" onclick={copySecret}>{t('common.copy')}</Button>
    </div>
    <p class="sc-totp__url">{setupUrl}</p>
    <TextField
      type="password"
      label={t('common.current_password')}
      bind:value={enrollPassword}
      autocomplete="current-password"
    />
    <TextField label={t('totp.6_digit_code')} bind:value={enrollCode} error={enrollError} />
  {/if}
  {#snippet actions()}
    <Button variant="text" onclick={closeEnroll}>{t('common.cancel')}</Button>
    <Button
      variant="filled"
      disabled={setupLoading || !enrollPassword || enrollCode.length !== 6}
      loading={enrolling}
      onclick={confirmEnroll}
    >
      {t('totp.enable')}
    </Button>
  {/snippet}
</Dialog>

<Dialog open={disableOpen} title={t('totp.turn_off_two_factor_authentication_2')} onclose={closeDisable}>
  <p>{t('totp.enter_your_current_password_continue')}</p>
  <TextField
    type="password"
    label={t('common.current_password')}
    bind:value={disablePassword}
    error={disableError}
    autocomplete="current-password"
  />
  {#snippet actions()}
    <Button variant="text" onclick={closeDisable}>{t('common.cancel')}</Button>
    <Button variant="filled" disabled={!disablePassword} loading={disabling} onclick={confirmDisable}>
      {t('totp.turn_off')}
    </Button>
  {/snippet}
</Dialog>

<Dialog open={reissueOpen} title={t('totp.reissue_recovery_codes_2')} onclose={closeReissue}>
  <p class="sc-totp__reissue-warning">
    <Icon icon={icons.warning} size={16} />
    {t('totp.reissuing_invalidates_all_10_recovery')} <strong>{t('totp.at_once')}</strong>{t('totp.cannot_undone_new_codes_shown')}
  </p>
  <TextField
    type="password"
    label={t('common.current_password')}
    bind:value={reissuePassword}
    error={reissueError}
    autocomplete="current-password"
  />
  {#snippet actions()}
    <Button variant="text" onclick={closeReissue}>{t('common.cancel')}</Button>
    <Button variant="filled" disabled={!reissuePassword} loading={reissuing} onclick={confirmReissue}>
      {t('totp.reissue')}
    </Button>
  {/snippet}
</Dialog>

<Dialog open={!!recoveryCodes} title={t('totp.recovery_codes')} onclose={closeRecoveryCodes}>
  <p>
    <Icon icon={icons.warning} size={16} />
    {t('totp.each_code_works_once_save')}
  </p>
  <ul class="sc-totp__codes">
    {#each recoveryCodes ?? [] as code (code)}
      <li><code>{code}</code></li>
    {/each}
  </ul>
  {#snippet actions()}
    <Button variant="filled" onclick={closeRecoveryCodes}>{t('common.saved')}</Button>
  {/snippet}
</Dialog>

<style>
  .sc-totp__status {
    display: flex;
    align-items: center;
    gap: 16px;
  }
  .sc-totp__badge {
    display: inline-flex;
    align-items: center;
    height: 32px;
    padding-inline: 12px;
    border-radius: var(--m3-shape-full);
    background: var(--m3c-surface-container-highest);
    color: var(--m3c-on-surface-variant);
    @apply --m3-label-large;
  }
  .sc-totp__badge--on {
    background: var(--m3c-primary-container);
    color: var(--m3c-on-primary-container);
  }
  .sc-totp__recovery {
    display: flex;
    align-items: center;
    gap: 16px;
    margin-top: 16px;
  }
  .sc-totp__recovery-count {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 0;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-medium;
  }
  .sc-totp__recovery-count--low {
    color: var(--m3c-error);
    font-weight: 500;
  }
  .sc-totp__reissue-warning {
    display: flex;
    align-items: flex-start;
    gap: 8px;
    padding: 12px;
    border-radius: var(--m3-shape-extra-small);
    background: var(--m3c-error-container);
    color: var(--m3c-on-error-container);
    @apply --m3-body-medium;
  }
  .sc-totp__secret-row {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-block: 8px;
  }
  .sc-totp__secret {
    padding: 8px 12px;
    border-radius: var(--m3-shape-extra-small);
    background: var(--m3c-surface-container-highest);
    @apply --m3-body-medium;
    letter-spacing: 0.05em;
    user-select: all;
  }
  .sc-totp__url {
    overflow-wrap: anywhere;
    @apply --m3-body-small;
    color: var(--m3c-on-surface-variant);
  }
  .sc-totp__codes {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 8px;
    padding: 0;
    margin: 16px 0;
    list-style: none;
  }
  .sc-totp__codes code {
    display: block;
    padding: 8px 12px;
    border-radius: var(--m3-shape-extra-small);
    background: var(--m3c-surface-container-highest);
    text-align: center;
  }
</style>
