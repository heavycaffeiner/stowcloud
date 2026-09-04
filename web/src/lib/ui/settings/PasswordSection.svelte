<script lang="ts">
  // Password change: minimum 10 characters. On
  // change, the Argon2 hash and the NT hash (SMB) are re-derived together
  // (server side, sc-auth::change_password).
  import { t } from '../../i18n'
  import { api, ApiError } from '../../api/client'
  import { scorePasswordStrength } from '../../format/password-strength'
  import { isErr } from '../../store/core/fp'
  import { validatePasswordChange } from '../../store/slices/settings.slice'
  import Button from '../Button.svelte'
  import TextField from '../TextField.svelte'
  import ProgressLinear from '../ProgressLinear.svelte'

  interface Props {
    onchanged?: () => void
  }
  let { onchanged }: Props = $props()

  let currentPassword = $state('')
  let newPassword = $state('')
  let confirmPassword = $state('')
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
  }

  async function submit(e: SubmitEvent): Promise<void> {
    e.preventDefault()
    currentError = null
    newError = null
    success = false

    const validation = validatePasswordChange(currentPassword, newPassword, confirmPassword, MIN_LEN)
    if (isErr(validation)) {
      if (validation.error.type === 'too_short') {
        newError = t('password.must_at_least_characters', { min: validation.error.min })
      } else {
        newError = t('password.new_passwords_do_not_match')
      }
      return
    }
    submitting = true
    try {
      await api.changePassword(currentPassword, newPassword)
      success = true
      reset()
      onchanged?.()
    } catch (err) {
      if (err instanceof ApiError && err.code === 'auth.invalid_credentials') {
        currentError = t('password.current_password_incorrect')
      } else if (err instanceof ApiError && err.code === 'auth.weak_password') {
        const min = err.reasonNumber('min_length') ?? MIN_LEN
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

  <!-- No "sign out every other device" here. The server does not revoke other
       sessions when a password changes, so the control would set a flag
       nothing reads: a box that ticks and does nothing is worse than its
       absence, because it is believed. Signing another device out is done
       from the sessions list below, which really does it. -->

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
