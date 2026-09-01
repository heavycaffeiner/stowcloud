<script lang="ts">
  // First-run admin bootstrap screen — Top-level route
  // (not under `(app)`), reachable automatically when session bootstrapping
  // decides `first-run` (see (app)/+layout.svelte) and always reachable
  // manually via the link on /login, since the auto-detection depends on a
  // guessed error code from the not-yet-landed backend (see
  // lib/state/auth.svelte.ts).
  import { t } from '../../lib/i18n'
  import { goto } from '$app/navigation'
  import { createInitialAdmin, type SetupFinding } from '../../lib/api/setup'
  import { api, ApiError, isMock } from '../../lib/api/client'
  import { completeLogin } from '../../lib/state/auth-bootstrap'
  import { scorePasswordStrength } from '../../lib/format/password-strength'
  import Button from '../../lib/ui/Button.svelte'
  import ProgressLinear from '../../lib/ui/ProgressLinear.svelte'
  import TextField from '../../lib/ui/TextField.svelte'

  const MIN_PASSWORD_LENGTH = 10

  let token = $state('')
  let username = $state('')
  let password = $state('')
  let passwordConfirm = $state('')
  let submitting = $state(false)
  let errorMsg = $state<string | null>(null)
  let doneButLoginFailed = $state(false)

  // The deployment's first configuration. It is asked for here because this is
  // the one moment somebody is definitely looking at the screen: a host list
  // saved now is one they will not have to discover from a refused request
  // later. Everything but the host list may stay as it is.
  //
  // Prefilled with the name this page was opened under, which is right far
  // more often than it is wrong: an operator browsing to the box by address or
  // by its LAN name is naming the thing they will keep using.
  let appHosts = $state(typeof location === 'undefined' ? '' : location.hostname)
  let trustedProxies = $state('')
  let bind = $state('')
  let shareName = $state('')
  let sharePath = $state('')
  let warnings = $state<SetupFinding[]>([])
  let bindFailed = $state(false)

  function toList(v: string): string[] {
    return v
      .split(',')
      .map((x) => x.trim())
      .filter(Boolean)
  }

  // The catalogue renders the sentence; the server sends the key and its
  // placeholders. The keys cannot be seen at the call site.
  /* i18n */ 'settings.would_lock_you_out'
  /* i18n */ 'settings.proxy_range_is_everything'
  function warningText(f: SetupFinding): string {
    return t(f.reason, f.args ?? {})
  }

  const strength = $derived(scorePasswordStrength(password))

  const passwordError = $derived.by((): string | null => {
    if (password.length === 0) return null
    if (password.length < MIN_PASSWORD_LENGTH) return t('setup.password_must_at_least_characters', { min: MIN_PASSWORD_LENGTH })
    return null
  })
  const confirmError = $derived.by((): string | null => {
    if (passwordConfirm.length === 0) return null
    if (passwordConfirm !== password) return t('setup.passwords_do_not_match')
    return null
  })
  const canSubmit = $derived(
    !submitting &&
      token.trim().length > 0 &&
      username.trim().length > 0 &&
      password.length >= MIN_PASSWORD_LENGTH &&
      passwordConfirm === password &&
      // The host list is the origin check every later request passes. An empty
      // one is a server that answers for no name at all.
      toList(appHosts).length > 0 &&
      // A folder is either named whole or not at all: half of one is a save
      // that would be refused for a reason nobody typed.
      (shareName.trim() === '') === (sharePath.trim() === '')
  )

  /** Signs the new administrator in and lands them on the file browser. The
   *  account was just created with these exact credentials, so this is one
   *  step rather than a bounce through the login screen. */
  async function enter(): Promise<void> {
    const login = await api.login(username.trim(), password)
    if (login.required !== 'totp') {
      await completeLogin()
      await goto('/b/')
      return
    }
    // TOTP is never enabled on a freshly created account, but if the backend
    // somehow disagrees, don't strand the user: send them to the normal login
    // flow, which handles the second-factor step.
    doneButLoginFailed = true
  }

  function messageFor(err: unknown): string {
    if (err instanceof ApiError) {
      switch (err.code) {
        case 'setup.invalid_token':
          return t('setup.invalid_setup_token_check_server')
        case 'setup.completed':
          return t('setup.setup_already_complete')
        case 'setup.token_expired':
          return t('setup.setup_token_has_expired')
        default:
          // Deliberately not surfacing err.message here, it may be a raw,
          // untranslated string from the server and this UI is Korean-only.
          return t('setup.could_not_create_administrator_account')
      }
    }
    return t('setup.could_not_create_administrator_account_2')
  }

  async function submit(e: SubmitEvent): Promise<void> {
    e.preventDefault()
    if (!canSubmit) return
    errorMsg = null
    doneButLoginFailed = false
    submitting = true
    try {
      const result = await createInitialAdmin({
        token: token.trim(),
        username: username.trim(),
        password,
        app_hosts: toList(appHosts),
        trusted_proxies: toList(trustedProxies),
        bind: bind.trim() || undefined,
        first_share: shareName.trim() ? { name: shareName.trim(), host: sharePath.trim() } : undefined
      })
      warnings = result.warnings
      bindFailed = result.bind_failed === true
      // Something worth reading before the page navigates away from it. The
      // one that matters is a host list that does not name where this page is
      // being read from: correct behind a proxy, and a lockout otherwise, and
      // nothing here can tell which. Landing straight on the file browser
      // would put the person one refused request away from a message they
      // never saw.
      if (warnings.length > 0 || bindFailed) {
        submitting = false
        return
      }
      await enter()
    } catch (err) {
      errorMsg = messageFor(err)
    } finally {
      submitting = false
    }
  }
</script>

<svelte:head><title>{t('setup.initial_setup_stowcloud')}</title></svelte:head>

<div class="sc-auth-page">
  <form class="sc-auth-card" onsubmit={submit}>
    <h1 class="sc-auth-card__title">{t('setup.create_administrator_account')}</h1>
    <p class="sc-auth-card__subtitle">
      {t('setup.on_server_s_first_start')} <code>setup-token</code>
      {t('setup.file_data_directory')}
      {#if isMock}<br /><em>{t('setup.mock_mode_any_token_value')}</em>{/if}
    </p>

    {#if doneButLoginFailed}
      <p class="sc-auth-card__error" role="alert">
        {t('setup.administrator_account_has_been_created')} <a href="/login">{t('setup.go_sign_page')}</a>{t('setup.sign')}
      </p>
    {:else}
      <TextField label={t('setup.setup_token')} bind:value={token} autofocus autocomplete="off" />
      <TextField label={t('setup.administrator_username')} bind:value={username} autocomplete="username" />
      <TextField label={t('common.password')} type="password" bind:value={password} error={passwordError} autocomplete="new-password" />
      {#if password}
        <div class="sc-auth-card__strength">
          <ProgressLinear value={strength.ratio} tone={strength.tier} label={t('common.password_strength', { level: strength.label })} />
          <span class="sc-auth-card__strength-label">{strength.label}</span>
        </div>
      {/if}
      <TextField
        label={t('setup.confirm_password')}
        type="password"
        bind:value={passwordConfirm}
        error={confirmError}
        autocomplete="new-password"
      />

      <h2 class="sc-auth-card__section">{t('setup.how_this_server_is_reached')}</h2>
      <TextField label={t('server.app_hosts_comma_separated')} bind:value={appHosts} autocomplete="off" />
      <p class="sc-auth-card__hint">{t('setup.app_hosts_hint')}</p>
      <TextField label={t('server.trusted_proxies_comma_separated')} bind:value={trustedProxies} autocomplete="off" />
      <p class="sc-auth-card__hint">{t('setup.trusted_proxies_hint')}</p>
      <TextField label={t('server.bind_address')} bind:value={bind} autocomplete="off" placeholder="0.0.0.0:8443" />
      <p class="sc-auth-card__hint">{t('setup.bind_hint')}</p>

      <h2 class="sc-auth-card__section">{t('setup.first_shared_folder')}</h2>
      <p class="sc-auth-card__hint">{t('setup.first_share_hint')}</p>
      <TextField label={t('common.name')} bind:value={shareName} autocomplete="off" />
      <TextField label={t('folder_share.server_path')} bind:value={sharePath} autocomplete="off" />

      {#if errorMsg}
        <p class="sc-auth-card__error" role="alert">{errorMsg}</p>
      {/if}

      <!-- Setup finished and noticed something. The page stays here rather
           than navigating away, because a lockout warning nobody read is the
           same as no warning. -->
      {#if warnings.length > 0 || bindFailed}
        <div class="sc-auth-card__warning" role="status">
          {#if bindFailed}<p>{t('setup.bind_failed_still_on_old_address')}</p>{/if}
          {#each warnings as w, i (w.reason + i)}
            <p>{warningText(w)}</p>
          {/each}
        </div>
      {/if}

      <div class="sc-auth-card__actions">
        {#if warnings.length > 0 || bindFailed}
          <Button variant="filled" onclick={enter} loading={submitting}>{t('setup.continue_anyway')}</Button>
        {:else}
          <Button variant="filled" type="submit" disabled={!canSubmit} loading={submitting}>
            {t('setup.create_administrator_account')}
          </Button>
        {/if}
      </div>

      <a class="sc-auth-card__setup-link sc-focus-ring" href="/login">{t('setup.already_have_account_sign')}</a>
    {/if}
  </form>
</div>

<style>
  .sc-auth-page {
    display: flex;
    align-items: center;
    justify-content: center;
    /* See (app)/+layout.svelte's `.sc-app-shell` comment on `100dvh` vs
       `100vh` on mobile. */
    min-height: 100vh;
    min-height: 100dvh;
    padding: var(--sc-page-pad);
    background: var(--m3c-surface);
  }
  .sc-auth-card {
    display: flex;
    flex-direction: column;
    gap: 16px;
    width: min(440px, 100%);
    /* See login/+page.svelte — card padding is MD3's 24dp, independent of the
       page margin the two of them used to double up on. */
    padding: 24px;
    border-radius: var(--m3-shape-large);
    background: var(--m3c-surface-container-high);
    color: var(--m3c-on-surface);
    box-shadow: 0 8px 24px rgb(0 0 0 / 0.3);
  }
  .sc-auth-card__title {
    margin: 0;
    @apply --m3-headline-small;
    text-align: center;
  }
  .sc-auth-card__subtitle {
    margin: 0 0 8px;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
    text-align: center;
  }
  .sc-auth-card__strength {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-top: calc(-1 * 8px);
  }
  .sc-auth-card__strength-label {
    flex: 0 0 auto;
    min-width: 48px;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
  .sc-auth-card__error {
    margin: 0;
    padding: 12px 16px;
    border-radius: var(--m3-shape-small);
    background: var(--m3c-error-container);
    color: var(--m3c-on-error-container);
    @apply --m3-body-medium;
  }
  .sc-auth-card__section {
    margin: 8px 0 0;
    @apply --m3-title-small;
  }
  .sc-auth-card__hint {
    margin: calc(-1 * 8px) 0 0;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
  .sc-auth-card__warning {
    display: flex;
    flex-direction: column;
    gap: 8px;
    margin: 0;
    padding: 12px 16px;
    border-radius: var(--m3-shape-small);
    background: var(--m3c-surface-container-highest);
    color: var(--m3c-on-surface);
    @apply --m3-body-medium;
  }
  .sc-auth-card__warning p {
    margin: 0;
  }
  .sc-auth-card__actions {
    display: flex;
    align-items: center;
    justify-content: flex-end;
    margin-top: 8px;
  }
  .sc-auth-card__setup-link {
    margin-top: 16px;
    padding-block: 4px;
    color: var(--m3c-primary);
    @apply --m3-body-small;
    text-align: center;
    text-decoration: none;
  }
  .sc-auth-card__setup-link:hover {
    text-decoration: underline;
  }
</style>
