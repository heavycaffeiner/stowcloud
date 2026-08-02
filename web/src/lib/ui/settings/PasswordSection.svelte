<script lang="ts">
  // Password change — DESIGN-AUTH.md §2.3/§2.4: minimum 10 characters; on
  // change, the Argon2 hash and the NT hash (SMB) are re-derived together
  // (server side, sc-auth::change_password). Whether other devices get
  // logged out is the user's own choice — app passwords are never revoked
  // automatically (to avoid silently breaking a sync client).
  import { t } from '../../i18n'
  import { api, ApiError } from '../../api/client'
  import { scorePasswordStrength } from '../../format/password-strength'
  import Button from '../Button.svelte'
  import TextField from '../TextField.svelte'
  import Checkbox from '../Checkbox.svelte'
  import ProgressLinear from '../ProgressLinear.svelte'

  interface Props {
    onchanged?: () => void
  }
  let { onchanged }: Props = $props()

  let currentPassword = $state('')
  let newPassword = $state('')
  let confirmPassword = $state('')
  let revokeOtherSessions = $state(false)
  let submitting = $state(false)
  let currentError = $state<string | null>(null)
  let newError = $state<string | null>(null)
  let success = $state(false)

  const MIN_LEN = 10

  const strength = $derived(scorePasswordStrength(newPassword))

  function reset(): void {
    currentPassword = ''
    newPassword = ''
    confirmPassword = ''
    revokeOtherSessions = false
  }

  async function submit(e: SubmitEvent): Promise<void> {
    e.preventDefault()
    currentError = null
    newError = null
    success = false

    if (newPassword.length < MIN_LEN) {
      newError = t('password.must_at_least_characters', { min: MIN_LEN })
      return
    }
    if (newPassword !== confirmPassword) {
      newError = t('password.new_passwords_do_not_match')
      return
    }

    submitting = true
    try {
      await api.changePassword(currentPassword, newPassword, revokeOtherSessions)
      success = true
      reset()
      onchanged?.()
    } catch (err) {
      if (err instanceof ApiError && err.code === 'auth.invalid_credentials') {
        currentError = t('password.current_password_incorrect')
      } else if (err instanceof ApiError && err.code === 'auth.weak_password') {
        const min = (err.detail?.min_length as number | undefined) ?? MIN_LEN
        newError = t('password.must_at_least_characters', { min })
      } else {
        currentError = t('password.could_not_change_password_try')
      }
    } finally {
      submitting = false
    }
  }
</script>

<form class="sc-password-form" onsubmit={submit}>
  <TextField
    type="password"
    label={t('common.current_password')}
    bind:value={currentPassword}
    error={currentError}
    autocomplete="current-password"
  />
  <TextField
    type="password"
    label={t('password.new_password')}
    bind:value={newPassword}
    error={newError}
    autocomplete="new-password"
  />
  {#if newPassword}
    <div class="sc-password-form__strength">
      <ProgressLinear value={strength.ratio} tone={strength.tier} label={t('password.new_password_strength', { level: strength.label })} />
      <span class="sc-password-form__strength-label">{strength.label}</span>
    </div>
  {/if}
  <TextField type="password" label={t('password.confirm_new_password')} bind:value={confirmPassword} autocomplete="new-password" />

  <Checkbox
    bind:checked={revokeOtherSessions}
    label={t('password.sign_out_every_other_device')}
  />

  {#if success}
    <p class="sc-password-form__success" role="status">{t('password.password_changed')}</p>
  {/if}

  <div class="sc-password-form__actions">
    <Button type="submit" variant="filled" disabled={!currentPassword || !newPassword} loading={submitting}>
      {t('password.change_password')}
    </Button>
  </div>
</form>

<style>
  .sc-password-form {
    display: flex;
    flex-direction: column;
    gap: 16px;
    max-width: 400px;
  }
  .sc-password-form__actions {
    margin-top: 8px;
  }
  .sc-password-form__strength {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-top: calc(-1 * 8px);
  }
  .sc-password-form__strength-label {
    flex: 0 0 auto;
    min-width: 48px;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
  .sc-password-form__success {
    margin: 0;
    color: var(--m3c-primary);
    @apply --m3-body-medium;
  }
</style>
