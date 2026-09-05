<script lang="ts">
  // Password change: minimum 10 characters. On
  // change, the Argon2 hash and the NT hash (SMB) are re-derived together
  // (server side, sc-auth::change_password).
  import { createMutation } from '@tanstack/svelte-query'
  import { t } from '../../i18n'
  import { ApiError } from '../../api/client'
  import { describeApiError } from '../../api/error-text'
  import { scorePasswordStrength } from '../../format/password-strength'
  import { validatePasswordChange } from '../../format/password-change'
  import { changePasswordMutation } from '../../query/account'
  import Button from '../Button.svelte'
  import TextField from '../TextField.svelte'
  import ProgressLinear from '../ProgressLinear.svelte'

  let currentPassword = $state('')
  let newPassword = $state('')
  let confirmPassword = $state('')
  let currentError = $state<string | null>(null)
  let newError = $state<string | null>(null)
  let formError = $state<string | null>(null)
  let success = $state(false)

  const MIN_LEN = 10

  const strength = $derived(scorePasswordStrength(newPassword))
  const save = createMutation(() => changePasswordMutation())

  function reset(): void {
    currentPassword = ''
    newPassword = ''
    confirmPassword = ''
  }

  async function submit(e: SubmitEvent): Promise<void> {
    e.preventDefault()
    currentError = null
    newError = null
    formError = null
    success = false

    const problem = validatePasswordChange(newPassword, confirmPassword, MIN_LEN)
    if (problem) {
      newError =
        problem.kind === 'too_short'
          ? t('password.must_at_least_characters', { min: problem.min })
          : t('password.new_passwords_do_not_match')
      return
    }
    try {
      await save.mutateAsync({ current: currentPassword, next: newPassword })
      success = true
      reset()
    } catch (err) {
      if (err instanceof ApiError && err.code === 'auth.invalid_credentials') {
        currentError = t('password.current_password_incorrect')
      } else if (err instanceof ApiError && err.code === 'auth.weak_password') {
        const min = err.reasonNumber('min_length') ?? MIN_LEN
        newError = t('password.must_at_least_characters', { min })
      } else {
        formError = describeApiError(err, t('password.could_not_change_password_try'))
      }
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

  {#if formError}
    <p class="sc-password-form__error" role="alert">{formError}</p>
  {/if}
  {#if success}
    <p class="sc-password-form__success" role="status">{t('password.password_changed')}</p>
  {/if}

  <div class="sc-password-form__actions">
    <Button type="submit" variant="filled" disabled={!currentPassword || !newPassword} loading={save.isPending}>
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
  .sc-password-form__error {
    margin: 0;
    color: var(--m3c-error);
    @apply --m3-body-small;
  }
  .sc-password-form__success {
    margin: 0;
    color: var(--m3c-primary);
    @apply --m3-body-medium;
  }
</style>
